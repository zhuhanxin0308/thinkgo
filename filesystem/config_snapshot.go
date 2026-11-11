package filesystem

import "reflect"

type configCloneVisit struct {
	typeOf  reflect.Type
	pointer uintptr
	length  int
}

func cloneMap(source map[string]interface{}) map[string]interface{} {
	if source == nil {
		return make(map[string]interface{})
	}
	return cloneValue(source).(map[string]interface{})
}

// cloneValue 隔离强类型集合并保持类型、既有空值语义和循环引用；对象句柄沿用借用语义。
func cloneValue(value interface{}) interface{} {
	if value == nil {
		return nil
	}
	return cloneConfigCollection(reflect.ValueOf(value), make(map[configCloneVisit]reflect.Value)).Interface()
}

func cloneConfigCollection(value reflect.Value, seen map[configCloneVisit]reflect.Value) reflect.Value {
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return value
		}
		result := reflect.New(value.Type()).Elem()
		result.Set(cloneConfigCollection(value.Elem(), seen))
		return result
	case reflect.Map, reflect.Slice:
		if value.IsNil() {
			// 旧版动态容器返回可写空副本，其余强类型容器保留 nil。
			switch value.Type() {
			case reflect.TypeFor[map[string]interface{}]():
				return reflect.MakeMap(value.Type())
			case reflect.TypeFor[[]interface{}]():
				return reflect.MakeSlice(value.Type(), 0, 0)
			}
			return value
		}
		visit := configCloneVisit{value.Type(), value.Pointer(), value.Len()}
		if result, found := seen[visit]; found {
			return result
		}
		if value.Kind() == reflect.Map {
			result := reflect.MakeMapWithSize(value.Type(), value.Len())
			seen[visit] = result
			entries := value.MapRange()
			for entries.Next() {
				// 可比较键维持原有身份，只有作为配置值的集合需要隔离。
				result.SetMapIndex(entries.Key(), cloneConfigCollection(entries.Value(), seen))
			}
			return result
		}
		result := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		seen[visit] = result
		if !configElementContainsCollection(value.Type().Elem()) {
			reflect.Copy(result, value)
			return result
		}
		for index := range value.Len() {
			result.Index(index).Set(cloneConfigCollection(value.Index(index), seen))
		}
		return result
	case reflect.Array:
		result := reflect.New(value.Type()).Elem()
		if !configElementContainsCollection(value.Type().Elem()) {
			result.Set(value)
			return result
		}
		for index := range value.Len() {
			result.Index(index).Set(cloneConfigCollection(value.Index(index), seen))
		}
		return result
	default:
		return value
	}
}

func configElementContainsCollection(element reflect.Type) bool {
	for element.Kind() == reflect.Array {
		element = element.Elem()
	}
	switch element.Kind() {
	case reflect.Map, reflect.Slice, reflect.Interface:
		return true
	default:
		return false
	}
}
