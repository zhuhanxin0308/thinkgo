package binding

import (
	"encoding/json"
	"reflect"
	"strings"
)

// TypeDocumentation 保存具名类型及其 Go 字段的源码说明，不改变绑定或验证规则。
type TypeDocumentation struct {
	Description string                       `json:"description,omitempty"`
	Fields      map[string]string            `json:"fields,omitempty"`
	Inline      map[string]TypeDocumentation `json:"inline,omitempty"`
}

// Documentation 以完整导入路径和类型名索引说明，泛型实例复用原类型的说明。
type Documentation map[string]TypeDocumentation

// OperationWithDocumentation 仅为本次文档生成补充源码说明；调用期间元数据应保持只读。
// 显式 doc 标签优先，共享请求计划不会被修改。
func OperationWithDocumentation(input, output reflect.Type, operationID string, successStatus int, docs Documentation) (json.RawMessage, json.RawMessage, error) {
	return operationWithDocumentation(input, output, operationID, successStatus, docs)
}

func (docs Documentation) forType(typ reflect.Type) TypeDocumentation {
	if typ == nil {
		return TypeDocumentation{}
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	name := typ.Name()
	if position := strings.IndexByte(name, '['); position >= 0 {
		name = name[:position]
	}
	return docs[typ.PkgPath()+"."+name]
}

func (builder *schemaBuilder) documentedField(item field) field {
	if item.description == "" {
		item.description = builder.documentation.forType(item.owner).Fields[item.goName]
	}
	return item
}

// schemaWithInline 为匿名对象保留声明位置，防止相同结构在不同业务字段下串用说明。
func (builder *schemaBuilder) schemaWithInline(plan *node, docs TypeDocumentation, exists bool) map[string]any {
	if !exists || plan.rawJSON || plan.text || plan.binary || plan.number {
		return builder.schema(plan)
	}
	if plan.optional || plan.typ.Kind() == reflect.Pointer {
		return map[string]any{"anyOf": []any{builder.schemaWithInline(plan.elem, docs, true), map[string]any{"type": "null"}}}
	}
	switch plan.typ.Kind() {
	case reflect.Slice, reflect.Array:
		result := map[string]any{"type": []string{"array", "null"}, "items": builder.schemaWithInline(plan.elem, docs, true)}
		if plan.typ.Kind() == reflect.Array {
			result["type"], result["minItems"], result["maxItems"] = "array", plan.typ.Len(), plan.typ.Len()
		}
		return result
	case reflect.Map:
		return map[string]any{"type": []string{"object", "null"}, "additionalProperties": builder.schemaWithInline(plan.elem, docs, true)}
	case reflect.Struct:
		if plan.typ.Name() != "" {
			return builder.schema(plan)
		}
		properties := make(map[string]any)
		for _, item := range plan.fields {
			properties[item.name] = builder.fieldSchemaWithDocumentation(item, docs)
		}
		result := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
		builder.objectPresence(plan, result)
		if docs.Description != "" {
			result["description"] = docs.Description
		}
		return result
	}
	return builder.schema(plan)
}
