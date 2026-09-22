package binding

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/framework/validate"
)

const schemaDigestBytes = 8

type schemaBuilder struct {
	components    map[string]any
	names         map[*node]string
	output        bool
	documentation Documentation
}

// Operation 从同一输入类型与返回类型导出独立的 OpenAPI 操作 JSON 及可复用组件 JSON。
// 无法用标准 Schema 准确表达的规则保留在 x-thinkgo-validation，运行时仍完整验证。
func Operation(input, output reflect.Type, operationID string, successStatus int) (json.RawMessage, json.RawMessage, error) {
	return operationWithDocumentation(input, output, operationID, successStatus, nil)
}

func operationWithDocumentation(input, output reflect.Type, operationID string, successStatus int, docs Documentation) (json.RawMessage, json.RawMessage, error) {
	if strings.TrimSpace(operationID) == "" || successStatus < http.StatusOK || successStatus >= http.StatusMultipleChoices {
		return nil, nil, fmt.Errorf("%w: 操作标识或成功状态无效", ErrDefinition)
	}
	plan, err := planFor(input)
	if err != nil {
		return nil, nil, err
	}
	builder := schemaBuilder{components: make(map[string]any), names: make(map[*node]string), documentation: docs}
	parameters := make([]any, 0)
	bodyFields := make([]field, 0)
	formFields := make([]field, 0)
	for _, item := range plan.fields {
		item = builder.documentedField(item)
		switch item.source {
		case "body":
			bodyFields = append(bodyFields, item)
		case "form":
			formFields = append(formFields, item)
		default:
			location := item.source
			schema := builder.fieldSchema(item)
			parameter := map[string]any{"name": item.name, "in": location, "required": location == "path" || ruleRequired(item.rules), "schema": schema}
			if item.description != "" {
				parameter["description"] = item.description
			}
			kind := underlyingNode(item.node).typ.Kind()
			if !underlyingNode(item.node).binary && (kind == reflect.Slice || kind == reflect.Array) {
				parameter["style"], parameter["explode"] = "form", true
				if location == "header" || location == "path" {
					parameter["style"], parameter["explode"] = "simple", false
				}
				if location == "cookie" {
					parameter["explode"] = false
				}
			}
			parameters = append(parameters, parameter)
		}
	}
	if len(bodyFields) > 0 && len(formFields) > 0 {
		return nil, nil, fmt.Errorf("%w: JSON 和表单来源不能混用", ErrDefinition)
	}
	document := map[string]any{"operationId": operationID, "parameters": parameters}
	fields, mediaType := bodyFields, "application/json"
	if len(formFields) > 0 {
		fields, mediaType = formFields, "application/x-www-form-urlencoded"
	}
	if len(fields) > 0 {
		bodyPlan := &node{typ: plan.typ, fields: fields}
		required := false
		for _, item := range fields {
			required = required || ruleRequired(item.rules)
		}
		media := map[string]any{"schema": builder.objectSchema(bodyPlan, "request_"+mediaType)}
		content := map[string]any{mediaType: media}
		if len(formFields) > 0 {
			content["multipart/form-data"] = media
		}
		document["requestBody"] = map[string]any{"required": required, "content": content}
	}
	response := map[string]any{"description": http.StatusText(successStatus)}
	if output != nil && successStatus != http.StatusNoContent {
		builder.output = true
		outputPlan, err := compileNode(output, false, true, make(map[planKey]*node), 0)
		if err != nil {
			return nil, nil, err
		}
		media := "application/json"
		if output.Kind() == reflect.String {
			media = "text/plain"
		}
		response["content"] = map[string]any{media: map[string]any{"schema": builder.schema(outputPlan)}}
	}
	responses := map[string]any{strconv.Itoa(successStatus): response}
	for _, status := range []int{http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity, http.StatusInternalServerError} {
		responses[strconv.Itoa(status)] = map[string]any{"description": http.StatusText(status), "content": map[string]any{"application/json": map[string]any{"schema": errorSchema()}, "text/plain": map[string]any{"schema": map[string]any{"type": "string"}}}}
	}
	document["responses"] = responses
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, nil, err
	}
	components, err := json.Marshal(builder.components)
	return encoded, components, err
}

func (builder *schemaBuilder) schema(plan *node) map[string]any {
	result := builder.schemaValue(plan)
	if description := builder.documentation.forType(plan.typ).Description; description != "" && result["$ref"] == nil {
		result["description"] = description
	}
	return result
}

func (builder *schemaBuilder) schemaValue(plan *node) map[string]any {
	if plan.rawJSON {
		return map[string]any{}
	}
	if plan.number {
		return map[string]any{"type": "number"}
	}
	if plan.optional || plan.typ.Kind() == reflect.Pointer {
		return map[string]any{"anyOf": []any{builder.schema(plan.elem), map[string]any{"type": "null"}}}
	}
	if plan.binary {
		return map[string]any{"type": []string{"string", "null"}, "contentEncoding": "base64"}
	}
	if plan.text {
		result := map[string]any{"type": "string"}
		if plan.typ.PkgPath() == "time" && plan.typ.Name() == "Time" {
			result["format"] = "date-time"
		}
		return result
	}
	switch plan.typ.Kind() {
	case reflect.Interface:
		return map[string]any{}
	case reflect.Struct:
		return builder.objectSchema(plan, "")
	case reflect.Slice, reflect.Array:
		result := map[string]any{"type": []string{"array", "null"}, "items": builder.schema(plan.elem)}
		if plan.typ.Kind() == reflect.Array {
			result["minItems"], result["maxItems"] = plan.typ.Len(), plan.typ.Len()
		}
		if plan.typ.Kind() == reflect.Array {
			result["type"] = "array"
		}
		return result
	case reflect.Map:
		return map[string]any{"type": []string{"object", "null"}, "additionalProperties": builder.schema(plan.elem)}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	default:
		result := map[string]any{"type": "integer"}
		bits := plan.typ.Bits()
		if plan.typ.Kind() >= reflect.Uint && plan.typ.Kind() <= reflect.Uint64 {
			result["minimum"] = 0
			result["maximum"] = json.Number(strconv.FormatUint(^uint64(0)>>(64-bits), 10))
		} else {
			// 直接缩减有符号整数的有效位宽，避免无符号转换产生溢出歧义。
			maximum := int64(math.MaxInt64) >> (64 - bits)
			result["minimum"] = json.Number(strconv.FormatInt(-maximum-1, 10))
			result["maximum"] = json.Number(strconv.FormatInt(maximum, 10))
		}
		return result
	}
}

func (builder *schemaBuilder) objectSchema(plan *node, suffix string) map[string]any {
	if name, exists := builder.names[plan]; exists {
		return map[string]any{"$ref": "#/components/schemas/" + name}
	}
	digest := sha256.Sum256([]byte(plan.typ.PkgPath() + "/" + plan.typ.String() + suffix + strconv.FormatBool(builder.output)))
	name := "ThinkGo_" + hex.EncodeToString(digest[:schemaDigestBytes])
	builder.names[plan] = name
	properties := make(map[string]any)
	object := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if description := builder.documentation.forType(plan.typ).Description; description != "" {
		object["description"] = description
	}
	builder.components[name] = object
	for _, item := range plan.fields {
		properties[item.name] = builder.fieldSchema(item)
	}
	builder.objectPresence(plan, object)
	return map[string]any{"$ref": "#/components/schemas/" + name}
}

func (builder *schemaBuilder) fieldSchema(item field) map[string]any {
	return builder.fieldSchemaWithDocumentation(item, builder.documentation.forType(item.owner))
}

func (builder *schemaBuilder) fieldSchemaWithDocumentation(item field, docs TypeDocumentation) map[string]any {
	if item.description == "" {
		item.description = docs.Fields[item.goName]
	}
	item = builder.documentedField(item)
	plan := item.node
	if ruleRequired(item.rules) {
		plan = underlyingNode(plan)
	}
	inline, exists := docs.Inline[item.goName]
	result := builder.schemaWithInline(plan, inline, exists)
	if ruleRequired(item.rules) {
		result["not"] = map[string]any{"type": "null"}
		kind := underlyingNode(item.node).typ.Kind()
		nonEmpty := make(map[string]any)
		if underlyingNode(item.node).binary || underlyingNode(item.node).text {
			kind = reflect.String
		}
		switch kind {
		case reflect.String:
			if !underlyingNode(item.node).number {
				nonEmpty["minLength"] = 1
			}
		case reflect.Array, reflect.Slice:
			if !underlyingNode(item.node).rawJSON {
				nonEmpty["minItems"] = 1
			}
		case reflect.Map:
			nonEmpty["minProperties"] = 1
		}
		if len(nonEmpty) > 0 {
			result["allOf"] = []any{nonEmpty}
		}
	}
	if item.description != "" {
		result["description"] = item.description
	}
	if item.hasDefault {
		result["default"] = item.defaultValue
	}
	if item.rules == "" {
		return quotedSchema(result, item)
	}
	result["x-thinkgo-validation"] = item.rules
	rules, _ := validate.DescribeRules(item.rules)
	kind := underlyingNode(item.node).typ.Kind()
	if underlyingNode(item.node).number {
		kind = reflect.Float64
	}
	if underlyingNode(item.node).binary || underlyingNode(item.node).text {
		kind = reflect.String
	}
	for _, rule := range rules {
		constraint := make(map[string]any)
		switch rule.Name {
		case "length":
			keys := []string{"minLength", "maxLength"}
			if kind == reflect.Slice || kind == reflect.Array {
				keys = []string{"minItems", "maxItems"}
			}
			if kind == reflect.Map {
				keys = []string{"minProperties", "maxProperties"}
			}
			if kind != reflect.String && kind != reflect.Slice && kind != reflect.Array && kind != reflect.Map {
				continue
			}
			constraint[keys[0]] = json.Number(rule.Arguments[0])
			constraint[keys[1]] = json.Number(rule.Arguments[len(rule.Arguments)-1])
		case "min", "max", "gt", "lt", "egt", "elt", "between":
			if kind < reflect.Int || kind > reflect.Float64 {
				continue
			}
			switch rule.Name {
			case "min", "egt":
				constraint["minimum"] = json.Number(rule.Parameter)
			case "max", "elt":
				constraint["maximum"] = json.Number(rule.Parameter)
			case "gt":
				constraint["exclusiveMinimum"] = json.Number(rule.Parameter)
			case "lt":
				constraint["exclusiveMaximum"] = json.Number(rule.Parameter)
			case "between":
				constraint["minimum"], constraint["maximum"] = json.Number(rule.Arguments[0]), json.Number(rule.Arguments[1])
			}
		case "in":
			// 数值集合规则使用原始十进制文本比较，不能无损转换为 JSON Schema 的数值相等。
			if kind != reflect.String && kind != reflect.Bool {
				continue
			}
			values := make([]any, 0, len(rule.Arguments))
			for _, value := range rule.Arguments {
				if kind == reflect.String {
					values = append(values, value)
				} else if kind == reflect.Bool {
					if value == "true" || value == "false" {
						values = append(values, value == "true")
					}
				}
			}
			if len(values) == len(rule.Arguments) {
				constraint["enum"] = values
			}
		}
		for key, value := range constraint {
			if _, exists := result[key]; exists {
				entries, _ := result["allOf"].([]any)
				result["allOf"] = append(entries, map[string]any{key: value})
			} else {
				result[key] = value
			}
		}
	}
	return quotedSchema(result, item)
}

func underlyingNode(plan *node) *node {
	for plan.optional || plan.typ.Kind() == reflect.Pointer {
		plan = plan.elem
	}
	return plan
}
func ruleRequired(specification string) bool {
	if specification == "" {
		return false
	}
	rules, _ := validate.DescribeRules(specification)
	for _, rule := range rules {
		if rule.Name == "required" {
			return true
		}
	}
	return false
}
func errorSchema() map[string]any {
	return map[string]any{"type": "object", "required": []string{"code", "msg", "data"}, "properties": map[string]any{
		"code": map[string]any{"type": "integer"}, "msg": map[string]any{"type": "string"}, "data": map[string]any{},
	}}
}
