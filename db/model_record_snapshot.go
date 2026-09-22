package db

import (
	"database/sql/driver"
	"fmt"
	"reflect"
	"time"
)

type recordSnapshotVisit struct {
	typ     reflect.Type
	pointer uintptr
}

// snapshotModelFields 只快照业务字段，避免复制模型锁、事务和请求上下文。
func snapshotModelFields(value reflect.Value) (map[string]any, error) {
	metadata, err := loadModelMetadata(value.Type())
	if err != nil {
		return nil, err
	}
	result := make(map[string]any, len(metadata.fields))
	for _, field := range metadata.fields {
		if field.readOnly {
			continue
		}
		v := modelFieldValue(value, field.indexPath, false)
		snapshot, err := snapshotRecordValue(v, make(map[recordSnapshotVisit]bool))
		if err != nil {
			return nil, fmt.Errorf("%w: 字段 %s 无法快照: %w", ErrInvalidModel, field.column, err)
		}
		result[field.column] = snapshot
	}
	return result, nil
}

// snapshotRecordValue 使用独立容器或 Valuer 的数据库值，防止可变字段原地修改漏检。
// 不能无损快照的字段明确报错，循环数据不会导致无限递归。
func snapshotRecordValue(value reflect.Value, visiting map[recordSnapshotVisit]bool) (any, error) {
	if !value.IsValid() {
		return nil, nil
	}
	if value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil, nil
		}
		return snapshotRecordValue(value.Elem(), visiting)
	}
	if value.Kind() == reflect.Pointer || value.Kind() == reflect.Map || value.Kind() == reflect.Slice {
		if value.IsNil() {
			return nil, nil
		}
		visit := recordSnapshotVisit{value.Type(), value.Pointer()}
		if visiting[visit] {
			return nil, fmt.Errorf("字段包含循环引用")
		}
		visiting[visit] = true
		defer delete(visiting, visit)
	}
	if value.CanInterface() {
		valuer, ok := value.Interface().(driver.Valuer)
		if !ok && value.CanAddr() {
			valuer, ok = value.Addr().Interface().(driver.Valuer)
		}
		if ok {
			data, err := valuer.Value()
			if err != nil {
				return nil, err
			}
			if !driver.IsValue(data) {
				return nil, fmt.Errorf("取值器返回了非法数据库值 %T", data)
			}
			return cloneDatabaseValue(data), nil
		}
		if value.Type() == reflect.TypeFor[time.Time]() {
			return value.Interface(), nil
		}
	}
	switch value.Kind() {
	case reflect.Pointer:
		return snapshotRecordValue(value.Elem(), visiting)
	case reflect.Struct:
		result := make(map[string]any, value.NumField())
		for i := 0; i < value.NumField(); i++ {
			field := value.Type().Field(i)
			if field.PkgPath != "" {
				return nil, fmt.Errorf("含私有状态的字段类型 %s 必须实现 driver.Valuer", value.Type())
			}
			item, err := snapshotRecordValue(value.Field(i), visiting)
			if err != nil {
				return nil, err
			}
			result[field.Name] = item
		}
		return result, nil
	case reflect.Slice, reflect.Array:
		result := make([]any, value.Len())
		for i := 0; i < value.Len(); i++ {
			item, err := snapshotRecordValue(value.Index(i), visiting)
			if err != nil {
				return nil, err
			}
			result[i] = item
		}
		return result, nil
	case reflect.Map:
		result := make(map[any]any, value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			key := iterator.Key()
			if key.Kind() == reflect.Pointer || key.Kind() == reflect.Interface {
				return nil, fmt.Errorf("快照不支持指针或接口作为映射键")
			}
			item, err := snapshotRecordValue(iterator.Value(), visiting)
			if err != nil {
				return nil, err
			}
			result[key.Interface()] = item
		}
		return result, nil
	case reflect.Chan, reflect.Func, reflect.UnsafePointer:
		return nil, fmt.Errorf("不支持字段类型 %s", value.Type())
	default:
		return value.Interface(), nil
	}
}
