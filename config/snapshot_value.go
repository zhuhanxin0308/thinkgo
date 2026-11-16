package config

import (
	"cmp"
	"fmt"
	"io"
	"reflect"
	"slices"
	"sync"
	"time"
)

type configSnapshotVisit struct {
	typ      reflect.Type
	pointer  uintptr
	length   int
	capacity int
}

type configSnapshotCopy struct {
	value  reflect.Value
	height int
}

type configSnapshotSpan struct {
	start, end uintptr
}

type configSnapshotCopier struct {
	visited map[configSnapshotVisit]configSnapshotCopy
	spans   *[]configSnapshotSpan
}

func copyTypedConfigValue(input interface{}, depth int, spans *[]configSnapshotSpan) (interface{}, error) {
	if input == nil {
		return nil, nil
	}
	value := reflect.ValueOf(input)
	copier := configSnapshotCopier{spans: spans}
	deepest := depth
	copied, err := copier.copyValue(value, depth, &deepest)
	if err != nil {
		return nil, err
	}
	return copied.Interface(), nil
}

func (copier *configSnapshotCopier) copyValue(value reflect.Value, depth int, deepest *int) (reflect.Value, error) {
	if depth > maxConfigJSONDepth {
		return reflect.Value{}, fmt.Errorf("%w: 配置值嵌套过深", ErrConfigUnsupportedValue)
	}
	*deepest = max(*deepest, depth)
	typ := value.Type()
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return value, nil
		}
		copied, err := copier.copyValue(value.Elem(), depth+1, deepest)
		if err != nil {
			return reflect.Value{}, err
		}
		result := reflect.New(typ).Elem()
		result.Set(copied)
		return result, nil
	case reflect.Pointer, reflect.Map, reflect.Slice:
		return copier.copyReference(value, depth, deepest)
	case reflect.Array:
		result := reflect.New(typ).Elem()
		return copier.copyElements(result, value, depth, deepest)
	case reflect.Struct:
		return copier.copyStruct(value, depth, deepest)
	case reflect.Chan, reflect.Func, reflect.UnsafePointer:
		return reflect.Value{}, fmt.Errorf("%w: %s", ErrConfigUnsupportedValue, typ)
	default:
		return value, nil
	}
}

func (copier *configSnapshotCopier) copyReference(value reflect.Value, depth int, deepest *int) (reflect.Value, error) {
	if value.IsNil() {
		return value, nil
	}
	typ := value.Type()
	visit := configSnapshotVisit{typ: typ, pointer: value.Pointer()}
	if value.Kind() == reflect.Slice {
		visit.length, visit.capacity = value.Len(), value.Cap()
	}
	if copied, exists := copier.visited[visit]; exists {
		if !copied.value.IsValid() {
			return reflect.Value{}, fmt.Errorf("%w: %s 含有循环引用", ErrConfigUnsupportedValue, typ)
		}
		if depth+copied.height > maxConfigJSONDepth {
			return reflect.Value{}, fmt.Errorf("%w: 配置值嵌套过深", ErrConfigUnsupportedValue)
		}
		*deepest = max(*deepest, depth+copied.height)
		return copied.value, nil
	}
	// 尚未完成的节点只作为循环检测标记。完成后复用副本，保留已共享的引用。
	if copier.visited == nil {
		copier.visited = make(map[configSnapshotVisit]configSnapshotCopy)
	}
	copier.visited[visit] = configSnapshotCopy{}
	recordConfigSnapshotSpan(copier.spans, value)
	branchDepth := depth
	var result reflect.Value
	switch value.Kind() {
	case reflect.Pointer:
		elem, err := copier.copyValue(value.Elem(), depth+1, &branchDepth)
		if err != nil {
			return reflect.Value{}, err
		}
		result = reflect.New(typ.Elem()).Convert(typ)
		result.Elem().Set(elem)
	case reflect.Map:
		result = reflect.MakeMapWithSize(typ, value.Len())
		entries := value.MapRange()
		for entries.Next() {
			key := entries.Key()
			if !configValueImmutable(key, depth+1, &branchDepth) {
				return reflect.Value{}, fmt.Errorf("%w: %s 的键包含可变引用", ErrConfigUnsupportedValue, typ)
			}
			item, err := copier.copyValue(entries.Value(), depth+1, &branchDepth)
			if err != nil {
				return reflect.Value{}, err
			}
			result.SetMapIndex(key, item)
		}
	case reflect.Slice:
		var err error
		result, err = copier.copyElements(reflect.MakeSlice(typ, value.Len(), value.Len()), value, depth, &branchDepth)
		if err != nil {
			return reflect.Value{}, err
		}
	}
	copier.visited[visit] = configSnapshotCopy{value: result, height: branchDepth - depth}
	*deepest = max(*deepest, branchDepth)
	return result, nil
}

func (copier *configSnapshotCopier) copyElements(result, value reflect.Value, depth int, deepest *int) (reflect.Value, error) {
	for index := range value.Len() {
		item, err := copier.copyValue(value.Index(index), depth+1, deepest)
		if err != nil {
			return reflect.Value{}, err
		}
		result.Index(index).Set(item)
	}
	return result, nil
}

func (copier *configSnapshotCopier) copyStruct(value reflect.Value, depth int, deepest *int) (reflect.Value, error) {
	typ := value.Type()
	if typ == reflect.TypeFor[time.Time]() {
		return value, nil
	}
	if configStructHasIdentity(typ) {
		return reflect.Value{}, fmt.Errorf("%w: %s 含有锁或资源状态", ErrConfigUnsupportedValue, typ)
	}
	result := reflect.New(typ).Elem()
	hasPrivateFields := false
	for index := range value.NumField() {
		field := value.Field(index)
		if !typ.Field(index).IsExported() {
			if !configValueImmutable(field, depth+1, deepest) {
				return reflect.Value{}, fmt.Errorf("%w: %s.%s 为私有可变字段", ErrConfigUnsupportedValue, typ, typ.Field(index).Name)
			}
			hasPrivateFields = true
			continue
		}
		copied, err := copier.copyValue(field, depth+1, deepest)
		if err != nil {
			return reflect.Value{}, err
		}
		result.Field(index).Set(copied)
	}
	if !hasPrivateFields {
		return result, nil
	}
	// 所有字段验证完成后保留安全的私有字段，再以已隔离的公开字段覆盖原引用。
	withPrivate := reflect.New(typ).Elem()
	withPrivate.Set(value)
	for index := range value.NumField() {
		if typ.Field(index).IsExported() {
			withPrivate.Field(index).Set(result.Field(index))
		}
	}
	return withPrivate, nil
}

func configValueImmutable(value reflect.Value, depth int, deepest *int) bool {
	if depth > maxConfigJSONDepth {
		return false
	}
	*deepest = max(*deepest, depth)
	switch value.Kind() {
	case reflect.Bool, reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128:
		return true
	case reflect.Interface:
		return value.IsNil() || configValueImmutable(value.Elem(), depth+1, deepest)
	case reflect.Struct:
		if value.Type() == reflect.TypeFor[time.Time]() {
			return true
		}
		if configStructHasIdentity(value.Type()) {
			return false
		}
		for index := range value.NumField() {
			if !configValueImmutable(value.Field(index), depth+1, deepest) {
				return false
			}
		}
		return true
	case reflect.Array:
		for index := range value.Len() {
			if !configValueImmutable(value.Index(index), depth+1, deepest) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func configStructHasIdentity(typ reflect.Type) bool {
	pointer := reflect.PointerTo(typ)
	// 原子类型的 noCopy 标记也实现 Locker 接口，锁和资源对象不能复制。
	return pointer.Implements(reflect.TypeFor[sync.Locker]()) || pointer.Implements(reflect.TypeFor[io.Closer]())
}

// recordConfigSnapshotSpan 记录可访问的指针和切片存储范围，用于拒绝无法保留的内部别名。
func recordConfigSnapshotSpan(spans *[]configSnapshotSpan, value reflect.Value) {
	if spans == nil {
		return
	}
	var size uintptr
	switch value.Kind() {
	case reflect.Pointer:
		size = value.Type().Elem().Size()
	case reflect.Slice:
		size = uintptr(value.Cap()) * value.Type().Elem().Size()
	}
	if size != 0 {
		start := value.Pointer()
		*spans = append(*spans, configSnapshotSpan{start: start, end: start + size})
	}
}

func validateConfigSnapshotSpans(spans []configSnapshotSpan) error {
	slices.SortFunc(spans, func(left, right configSnapshotSpan) int {
		return cmp.Compare(left.start, right.start)
	})
	var end uintptr
	for _, span := range spans {
		if span.start < end {
			return fmt.Errorf("%w: 指针或切片视图含有重叠存储", ErrConfigUnsupportedValue)
		}
		end = span.end
	}
	return nil
}
