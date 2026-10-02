package openapi

import (
	"bytes"
	"encoding/json"
)

// WithValidationRules 控制 Schema 中 x-thinkgo-validation 规则原文的导出，默认保留。
// 关闭时仍保留标准 Schema 约束，不改变运行时绑定及校验；示例和业务字段名不被过滤。
// 选项仅应传给 NewRegistry，创建后不提供动态修改接口。
func WithValidationRules(enabled bool) RegistryOption {
	return func(registry *Registry) error {
		if registry == nil {
			return ErrInvalidDocument
		}
		registry.mu.Lock()
		defer registry.mu.Unlock()
		if registry.frozen {
			return ErrRegistryFrozen
		}
		registry.omitValidationRules = !enabled
		return nil
	}
}

// withoutValidationRules 仅遍历 OpenAPI 中的 Schema 位置，不能全局删除同名 JSON 键。
// 在独立序列化副本上过滤，保留原注册内容，UseNumber 避免大整数示例损失精度。
func withoutValidationRules(encoded []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	components := schemaObject(document["components"])
	for _, schema := range schemaObject(components["schemas"]) {
		omitSchemaValidationRules(schema)
	}
	for _, key := range []string{"parameters", "headers"} {
		for _, parameter := range schemaObject(components[key]) {
			omitParameterValidationRules(parameter)
		}
	}
	for _, body := range schemaObject(components["requestBodies"]) {
		omitContentValidationRules(schemaObject(body)["content"])
	}
	for _, response := range schemaObject(components["responses"]) {
		omitResponseValidationRules(response)
	}
	for _, callback := range schemaObject(components["callbacks"]) {
		omitPathsValidationRules(callback)
	}
	omitPathsValidationRules(components["pathItems"])
	omitPathsValidationRules(document["paths"])
	omitPathsValidationRules(document["webhooks"])
	return json.Marshal(document)
}

func schemaObject(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

func omitPathsValidationRules(value any) {
	for _, path := range schemaObject(value) {
		item := schemaObject(path)
		omitParametersValidationRules(item["parameters"])
		for _, method := range []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"} {
			operation := schemaObject(item[method])
			omitParametersValidationRules(operation["parameters"])
			omitContentValidationRules(schemaObject(operation["requestBody"])["content"])
			for _, response := range schemaObject(operation["responses"]) {
				omitResponseValidationRules(response)
			}
			for _, callback := range schemaObject(operation["callbacks"]) {
				omitPathsValidationRules(callback)
			}
		}
	}
}

func omitParametersValidationRules(value any) {
	parameters, _ := value.([]any)
	for _, parameter := range parameters {
		omitParameterValidationRules(parameter)
	}
}

func omitParameterValidationRules(value any) {
	parameter := schemaObject(value)
	omitSchemaValidationRules(parameter["schema"])
	omitContentValidationRules(parameter["content"])
}

func omitResponseValidationRules(value any) {
	response := schemaObject(value)
	omitContentValidationRules(response["content"])
	for _, header := range schemaObject(response["headers"]) {
		omitParameterValidationRules(header)
	}
}

func omitContentValidationRules(value any) {
	for _, media := range schemaObject(value) {
		content := schemaObject(media)
		omitSchemaValidationRules(content["schema"])
		for _, encoding := range schemaObject(content["encoding"]) {
			for _, header := range schemaObject(schemaObject(encoding)["headers"]) {
				omitParameterValidationRules(header)
			}
		}
	}
}

func omitSchemaValidationRules(value any) {
	schema := schemaObject(value)
	if schema == nil {
		return
	}
	delete(schema, "x-thinkgo-validation")
	// 字段名、组件名或示例中的同名键不是注解。仅对这些映射的值递归。
	for _, key := range []string{"properties", "patternProperties", "$defs", "definitions", "dependentSchemas", "dependencies"} {
		for _, child := range schemaObject(schema[key]) {
			omitSchemaValidationRules(child)
		}
	}
	for _, key := range []string{"items", "not", "if", "then", "else", "contains", "propertyNames", "additionalProperties", "additionalItems", "unevaluatedProperties", "unevaluatedItems", "contentSchema"} {
		omitSchemaValidationRules(schema[key])
	}
	for _, key := range []string{"allOf", "anyOf", "oneOf", "prefixItems", "items"} {
		children, _ := schema[key].([]any)
		for _, child := range children {
			omitSchemaValidationRules(child)
		}
	}
}
