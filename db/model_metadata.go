package db

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"reflect"
	"sync"
	"time"
)

type modelFieldMetadata struct {
	indexPath []int
	column    string
	omitEmpty bool
	readOnly  bool
}

type modelMetadata struct {
	fields    []modelFieldMetadata
	modelPath []int
}

type modelMetadataCacheEntry struct {
	metadata modelMetadata
	err      error
}

var modelMetadataCache sync.Map

// ValidateModelType 仅校验可自动绑定的模型类型，不连接数据库或执行业务配置钩子。
// 普通查询 DTO 和 schema 预热应使用 PrewarmModelMetadata。
func ValidateModelType(typ reflect.Type) error {
	for typ != nil && typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	metadata, err := loadModelMetadata(typ)
	if err != nil {
		return err
	}
	if metadata.modelPath == nil {
		return fmt.Errorf("%w: 模型必须匿名嵌入 *db.Model", ErrInvalidModel)
	}
	return nil
}

// PrewarmModelMetadata 校验并预热自动发现模型的字段映射。
// 应用初始化在目标进程调用；schema:validate 可复用相同校验规则。
func PrewarmModelMetadata(types ...reflect.Type) error {
	for index, typ := range types {
		for typ != nil && typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}
		if typ == nil || typ.Kind() != reflect.Struct {
			return fmt.Errorf("%w: 第 %d 个模型类型必须是结构体", ErrInvalidModel, index+1)
		}
		if _, err := loadModelMetadata(typ); err != nil {
			return fmt.Errorf("预热模型 %s 失败: %w", typ.String(), err)
		}
	}
	return nil
}

func buildModelMetadata(typ reflect.Type) (modelMetadata, error) {
	if typ == nil || typ.Kind() != reflect.Struct {
		return modelMetadata{}, fmt.Errorf("%w: 元数据目标必须是结构体", ErrInvalidModel)
	}
	metadata := modelMetadata{fields: make([]modelFieldMetadata, 0, typ.NumField())}
	columns := make(map[string]string, typ.NumField())
	visiting := make(map[reflect.Type]bool)
	var walk func(reflect.Type, []int) error
	walk = func(current reflect.Type, path []int) error {
		if visiting[current] {
			return fmt.Errorf("%w: 模型包含循环匿名嵌入 %s", ErrInvalidModel, current)
		}
		visiting[current] = true
		defer delete(visiting, current)
		for index := 0; index < current.NumField(); index++ {
			field := current.Field(index)
			if field.PkgPath != "" {
				continue
			}
			fieldPath := append(append([]int(nil), path...), index)
			if field.Type == reflect.TypeFor[*Model]() {
				if !field.Anonymous {
					continue
				}
				if metadata.modelPath != nil {
					return fmt.Errorf("%w: 只允许嵌入一个 *db.Model", ErrInvalidModel)
				}
				metadata.modelPath = fieldPath
				continue
			}
			if field.Type == reflect.TypeFor[Model]() {
				return fmt.Errorf("%w: 请嵌入 *db.Model，不能复制包含锁的 db.Model", ErrInvalidModel)
			}
			name, options := parseStructTag(field.Tag.Get("thinkgo"))
			if name == "-" {
				continue
			}
			for option := range options {
				if option != "omitempty" && option != "readonly" {
					return fmt.Errorf("%w: 字段 %s 使用未知标签选项 %q", ErrInvalidModel, field.Name, option)
				}
			}
			underlying := field.Type
			if underlying.Kind() == reflect.Pointer {
				underlying = underlying.Elem()
			}
			if field.Anonymous && name == "" && underlying.Kind() == reflect.Struct && !isModelScalar(field.Type) {
				if len(options) != 0 {
					return fmt.Errorf("%w: 匿名嵌入不能使用字段选项", ErrInvalidModel)
				}
				if err := walk(underlying, fieldPath); err != nil {
					return err
				}
				continue
			}
			if name == "" {
				name = ToSnakeCase(field.Name)
			}
			if err := validateIdentifier(name); err != nil {
				return fmt.Errorf("%w: 字段 %s 的列名非法: %w", ErrInvalidModel, field.Name, err)
			}
			if previous, duplicated := columns[name]; duplicated {
				return fmt.Errorf("%w: 字段 %s 与 %s 映射到重复列 %q", ErrInvalidModel, previous, field.Name, name)
			}
			columns[name] = field.Name
			metadata.fields = append(metadata.fields, modelFieldMetadata{indexPath: fieldPath, column: name, omitEmpty: options["omitempty"], readOnly: options["readonly"]})
		}
		return nil
	}
	if err := walk(typ, nil); err != nil {
		return modelMetadata{}, err
	}
	return metadata, nil
}

// isModelScalar 保留数据库扫描器和取值器的整体字段语义。
func isModelScalar(typ reflect.Type) bool {
	for _, candidate := range []reflect.Type{typ, reflect.PointerTo(typ)} {
		if candidate.Implements(reflect.TypeFor[sql.Scanner]()) || candidate.Implements(reflect.TypeFor[driver.Valuer]()) {
			return true
		}
	}
	return typ == reflect.TypeFor[time.Time]() || typ == reflect.TypeFor[*time.Time]()
}

// modelFieldValue 在需要写入时才分配匿名指针，读取 nil 嵌入时返回零值字段。
func modelFieldValue(value reflect.Value, path []int, allocate bool) reflect.Value {
	for _, index := range path {
		if value.Kind() == reflect.Pointer {
			if value.IsNil() {
				if !allocate {
					value = reflect.Zero(value.Type().Elem())
				} else {
					value.Set(reflect.New(value.Type().Elem()))
					value = value.Elem()
				}
			} else {
				value = value.Elem()
			}
		}
		value = value.Field(index)
	}
	return value
}

func loadModelMetadata(typ reflect.Type) (modelMetadata, error) {
	if cached, ok := modelMetadataCache.Load(typ); ok {
		entry := cached.(modelMetadataCacheEntry)
		return entry.metadata, entry.err
	}
	metadata, err := buildModelMetadata(typ)
	entry := modelMetadataCacheEntry{metadata: metadata, err: err}
	actual, _ := modelMetadataCache.LoadOrStore(typ, entry)
	stored := actual.(modelMetadataCacheEntry)
	return stored.metadata, stored.err
}

func cloneModelMetadata(metadata modelMetadata) modelMetadata {
	cloned := modelMetadata{fields: append([]modelFieldMetadata(nil), metadata.fields...), modelPath: append([]int(nil), metadata.modelPath...)}
	for i := range cloned.fields {
		cloned.fields[i].indexPath = append([]int(nil), cloned.fields[i].indexPath...)
	}
	return cloned
}

// cachedModelMetadata 返回独立快照，生产转换使用缓存中的不可变元数据。
func cachedModelMetadata(typ reflect.Type) (modelMetadata, error) {
	metadata, err := loadModelMetadata(typ)
	if err != nil {
		return modelMetadata{}, err
	}
	return cloneModelMetadata(metadata), nil
}
