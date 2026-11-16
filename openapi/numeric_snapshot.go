package openapi

import (
	"reflect"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// restoreExactOpenAPILiterals 用精确 JSON 值恢复扩展、默认值和示例等非结构化字段。
func restoreExactOpenAPILiterals(target reflect.Value, exact any) {
	if exact == nil {
		return
	}
	switch target.Kind() {
	case reflect.Interface:
		if target.Type().NumMethod() == 0 {
			target.Set(reflect.ValueOf(exact))
		}
	case reflect.Pointer:
		if target.IsNil() {
			return
		}
		// 这些类型以私有映射保留条目，Map 暴露可访问的条目值。
		switch object := target.Interface().(type) {
		case *openapi3.Responses:
			restoreOpenAPILiteralMap(reflect.ValueOf(object.Map()), schemaObject(exact))
		case interface {
			Map() map[string]*openapi3.PathItem
		}:
			restoreOpenAPILiteralMap(reflect.ValueOf(object.Map()), schemaObject(exact))
		}
		restoreExactOpenAPILiterals(target.Elem(), exact)
	case reflect.Struct:
		restoreOpenAPILiteralFields(target, schemaObject(exact))
	case reflect.Map:
		restoreOpenAPILiteralMap(target, schemaObject(exact))
	case reflect.Slice, reflect.Array:
		if list, ok := exact.([]any); ok && target.Len() == len(list) {
			for index, value := range list {
				restoreExactOpenAPILiterals(target.Index(index), value)
			}
		}
	}
}

func restoreOpenAPILiteralFields(target reflect.Value, exact map[string]any) {
	if exact == nil {
		return
	}
	typ := target.Type()
	for index := range target.NumField() {
		field := target.Field(index)
		if !field.CanSet() {
			continue
		}
		definition := typ.Field(index)
		name, _, _ := strings.Cut(definition.Tag.Get("json"), ",")
		switch {
		case definition.Name == "Extensions" && field.Kind() == reflect.Map:
			// 扩展在对象层平铺，保持解码器已选出的扩展名。
			restoreOpenAPILiteralMap(field, exact)
		case definition.Name == "Value" && strings.HasSuffix(typ.Name(), "Ref"):
			// 内联引用的 Value 直接使用引用对象的 JSON 数据。
			restoreExactOpenAPILiterals(field, exact)
		case name == "" && definition.Anonymous:
			restoreExactOpenAPILiterals(field, exact)
		case name != "-":
			if name == "" {
				name = definition.Name
			}
			restoreExactOpenAPILiterals(field, exact[name])
		}
	}
}

func restoreOpenAPILiteralMap(target reflect.Value, exact map[string]any) {
	if exact == nil || target.Type().Key().Kind() != reflect.String {
		return
	}
	entries := target.MapRange()
	for entries.Next() {
		value := exact[entries.Key().String()]
		if value == nil {
			continue
		}
		entry := entries.Value()
		if entry.Kind() == reflect.Pointer {
			restoreExactOpenAPILiterals(entry, value)
			continue
		}
		// 映射中的结构体和接口值不可寻址，恢复到副本后写回。
		copied := reflect.New(entry.Type()).Elem()
		copied.Set(entry)
		restoreExactOpenAPILiterals(copied, value)
		target.SetMapIndex(entries.Key(), copied)
	}
}
