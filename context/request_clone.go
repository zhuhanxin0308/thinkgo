package context

import "reflect"

// requestCloneVisit 标识容器视图，长度避免把共享起点的不同切片误认为同一值。
type requestCloneVisit struct {
	typeOf   reflect.Type
	pointer  uintptr
	length   int
	filtered bool
}

// cloneRequestValueGraph 复制 map、slice、array 及嵌套接口中的集合，并保留循环引用。
// 结构体、对象指针、函数和通道作为不透明对象保留原值，不复制 Session 或锁等身份状态。
func cloneRequestValueGraph(value interface{}, filter func(string) string) interface{} {
	if value == nil {
		return nil
	}
	return cloneRequestReflectValue(reflect.ValueOf(value), make(map[requestCloneVisit]reflect.Value), filter).Interface()
}

func cloneRequestReflectValue(value reflect.Value, seen map[requestCloneVisit]reflect.Value, filter func(string) string) reflect.Value {
	if value.Kind() == reflect.Interface {
		if value.IsNil() {
			return value
		}
		result := reflect.New(value.Type()).Elem()
		result.Set(cloneRequestReflectValue(value.Elem(), seen, filter))
		return result
	}
	if filter != nil {
		// 保持已有过滤契约：命名类型及其他强类型容器只复制，不隐式改变值。
		switch value.Interface().(type) {
		case string:
			return reflect.ValueOf(filter(value.String()))
		case []string, []interface{}, map[string]interface{}:
		default:
			filter = nil
		}
	}
	switch value.Kind() {
	case reflect.Map, reflect.Slice:
		if value.IsNil() {
			return value
		}
		visit := requestCloneVisit{value.Type(), value.Pointer(), value.Len(), filter != nil}
		if result, exists := seen[visit]; exists {
			return result
		}
		if value.Kind() == reflect.Map {
			result := reflect.MakeMapWithSize(value.Type(), value.Len())
			seen[visit] = result
			entries := value.MapRange()
			for entries.Next() {
				// 键是集合的身份，不转换指针键或包含对象身份的可比较键。
				result.SetMapIndex(entries.Key(), cloneRequestReflectValue(entries.Value(), seen, filter))
			}
			return result
		}
		result := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		seen[visit] = result
		for index := range value.Len() {
			result.Index(index).Set(cloneRequestReflectValue(value.Index(index), seen, filter))
		}
		return result
	case reflect.Array:
		result := reflect.New(value.Type()).Elem()
		for index := range value.Len() {
			result.Index(index).Set(cloneRequestReflectValue(value.Index(index), seen, nil))
		}
		return result
	default:
		return value
	}
}
