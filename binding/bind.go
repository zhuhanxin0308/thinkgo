package binding

import (
	"encoding"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"

	fwcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
	"github.com/zhuhanxin0308/thinkgo/framework/exception"
	"github.com/zhuhanxin0308/thinkgo/framework/validate"
)

// Issue 描述输入来源、字段路径和错误类别，不包含原始输入值。
type Issue struct {
	Field   string `json:"field"`
	Rule    string `json:"rule"`
	Message string `json:"message"`
}

const maximumIssues = 128

type bindState struct {
	issues        []Issue
	malformed     bool
	options       []validate.Option
	definitionErr error
}

// Bind 从字段标签指定的来源绑定并验证请求，成功后才替换目标对象。
// JSON 未声明字段和重复标量参数会失败；Optional 可保留 PATCH 的三态输入。
func Bind(request *fwcontext.Request, target any, options ...validate.Option) error {
	value := reflect.ValueOf(target)
	if request == nil || request.Raw() == nil || request.Raw().URL == nil || value.Kind() != reflect.Pointer || value.IsNil() || value.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("%w: 请求和结构体指针不能为空", ErrDefinition)
	}
	plan, err := planFor(value.Elem().Type())
	if err != nil {
		return err
	}
	// 零选项无需构造空验证器；显式选项仍在输入解析之前检查，保留定义错误优先级。
	if len(options) > 0 {
		if _, err := validate.NewValidator().Validate(nil, options...); err != nil {
			return fmt.Errorf("%w: %v", ErrDefinition, err)
		}
	}
	state := bindState{options: options}
	sources, err := request.SourcesFor(plan.sources)
	if err != nil {
		if errors.Is(err, fwcontext.ErrRequestBodyTooLarge) {
			return exception.NewHttpException(http.StatusRequestEntityTooLarge, "请求体超过大小上限")
		}
		if errors.Is(err, fwcontext.ErrInvalidQuery) {
			state.fail("query", "encoding", "查询参数编码无效")
		} else {
			state.fail("body", "encoding", "请求体编码无效")
		}
		return state.err()
	}
	body := sources.Body
	if plan.hasBody && plan.hasForm {
		return fmt.Errorf("%w: 同一请求不能混用 JSON 和表单字段", ErrDefinition)
	}
	if plan.hasBody {
		if sources.HasBody {
			if media := sources.MediaType; media != "application/json" && !strings.HasSuffix(media, "+json") {
				return exception.NewHttpException(http.StatusUnsupportedMediaType, "请求体必须使用 JSON")
			}
		}
	}
	query, form := sources.Query, sources.Form
	if plan.hasForm {
		if media := sources.MediaType; media != "application/x-www-form-urlencoded" && media != "multipart/form-data" {
			return exception.NewHttpException(http.StatusUnsupportedMediaType, "请求体必须使用表单编码")
		}
	}
	next := reflect.New(value.Elem().Type()).Elem()
	var data map[string]any
	if plan.validator != nil {
		data = make(map[string]any)
	}
	for _, item := range plan.fields {
		var raw any
		var exists bool
		var values []string
		switch item.source {
		case "body":
			raw, exists = body[item.name]
		case "path":
			raw, exists = request.RouteValue(item.name)
			if exists {
				values = []string{fmt.Sprint(raw)}
			}
		case "query":
			values, exists = query[item.name]
		case "form":
			values, exists = form[item.name]
		case "header":
			values, exists = sources.Header[http.CanonicalHeaderKey(item.name)]
		case "cookie":
			values, exists = sources.Cookies[item.name]
		}
		path := item.rootPath
		if exists && item.quoted {
			var valid bool
			raw, valid = state.unquote(raw, path)
			if !valid {
				continue
			}
		}
		if exists && item.source != "body" {
			kind := underlyingNode(item.node).typ.Kind()
			if !underlyingNode(item.node).binary && (item.source == "header" || item.source == "cookie" || item.source == "path") && (kind == reflect.Slice || kind == reflect.Array) {
				var expanded []string
				for _, value := range values {
					for _, part := range strings.Split(value, ",") {
						expanded = append(expanded, strings.TrimSpace(part))
					}
				}
				values = expanded
			}
			var valid bool
			raw, valid = state.parameter(item.node, values, path)
			if !valid {
				continue
			}
		}
		if !exists && item.hasDefault {
			raw, exists = item.defaultValue, true
		}
		if !exists && item.source == "path" {
			state.fail(path, "required", "缺少路径参数")
			continue
		}
		if !exists {
			continue
		}
		if raw != nil && data != nil {
			data[item.name] = raw
		}
		state.assign(item.node, fieldValue(next, item.index), raw, path, 0)
	}
	if plan.hasForm {
		unknownFields(&state, form, plan.knownFields, "form")
	} else if !plan.hasBody {
		// 仅外部来源 DTO 仍拒绝正文根字段，无需为必然拒绝的嵌套值创建深拷贝。
		sort.Strings(sources.BodyKeys)
		for _, key := range sources.BodyKeys {
			state.fail("body."+key, "unknown", "未声明的请求字段")
		}
	} else {
		unknownFields(&state, body, plan.knownFields, "body")
	}
	state.validate(plan, data, func(item field) string { return item.rootPath })
	if err := state.err(); err != nil {
		return err
	}
	value.Elem().Set(next)
	return nil
}

func fieldValue(value reflect.Value, index []int) reflect.Value {
	for _, position := range index {
		if value.Kind() == reflect.Pointer {
			if value.IsNil() {
				value.Set(reflect.New(value.Type().Elem()))
			}
			value = value.Elem()
		}
		value = value.Field(position)
	}
	return value
}

func (state *bindState) parameter(plan *node, values []string, path string) (any, bool) {
	if plan.optional || plan.typ.Kind() == reflect.Pointer {
		return state.parameter(plan.elem, values, path)
	}
	if plan.number {
		if len(values) == 1 && json.Valid([]byte(values[0])) && len(values[0]) > 0 && (values[0][0] == '-' || values[0][0] >= '0' && values[0][0] <= '9') {
			return json.Number(values[0]), true
		}
		state.fail(path, "type", "参数必须是 JSON 数值")
		return nil, false
	}
	if plan.binary {
		if len(values) != 1 {
			state.fail(path, "duplicate", "标量参数只能提交一次")
			return nil, false
		}
		return values[0], true
	}
	if plan.typ.Kind() == reflect.Slice || plan.typ.Kind() == reflect.Array {
		result := make([]any, 0, len(values))
		for index, text := range values {
			value, ok := state.parameter(plan.elem, []string{text}, fmt.Sprintf("%s[%d]", path, index))
			if !ok {
				return nil, false
			}
			result = append(result, value)
		}
		return result, true
	}
	if len(values) != 1 {
		state.fail(path, "duplicate", "标量参数只能提交一次")
		return nil, false
	}
	text := values[0]
	if plan.text || plan.typ.Kind() == reflect.String {
		return text, true
	}
	switch plan.typ.Kind() {
	case reflect.Bool:
		if text == "true" || text == "1" {
			return true, true
		}
		if text == "false" || text == "0" {
			return false, true
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if value, err := strconv.ParseInt(text, 10, plan.typ.Bits()); err == nil {
			return json.Number(strconv.FormatInt(value, 10)), true
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if value, err := strconv.ParseUint(text, 10, plan.typ.Bits()); err == nil {
			return json.Number(strconv.FormatUint(value, 10)), true
		}
	case reflect.Float32, reflect.Float64:
		if json.Valid([]byte(text)) {
			return json.Number(text), true
		}
	}
	state.fail(path, "type", "参数类型无效")
	return nil, false
}

func (state *bindState) assign(plan *node, target reflect.Value, raw any, path string, depth int) {
	if depth > maximumDepth {
		state.fail(path, "depth", "输入嵌套过深")
		return
	}
	if plan.optional {
		holder := target.Addr().Interface().(optionalValue)
		holder.setPresence(raw == nil)
		if raw != nil {
			state.assign(plan.elem, reflect.ValueOf(holder.valuePointer()).Elem(), raw, path, depth+1)
		}
		return
	}
	if plan.rawJSON {
		encoded, err := json.Marshal(raw)
		if err != nil {
			state.fail(path, "json", "JSON 字段编码无效")
			return
		}
		target.SetBytes(encoded)
		return
	}
	if plan.number {
		value, ok := raw.(json.Number)
		if !ok {
			state.fail(path, "type", "字段必须是 JSON 数值")
			return
		}
		target.SetString(value.String())
		return
	}
	if raw == nil {
		switch target.Kind() {
		case reflect.Pointer, reflect.Map, reflect.Slice:
			return
		}
		state.fail(path, "null", "该字段不接受 null")
		return
	}
	if target.Kind() == reflect.Pointer {
		target.Set(reflect.New(target.Type().Elem()))
		state.assign(plan.elem, target.Elem(), raw, path, depth+1)
		return
	}
	if plan.binary {
		text, ok := raw.(string)
		if !ok {
			state.fail(path, "type", "字节字段必须是 base64 字符串")
			return
		}
		decoded, err := base64.StdEncoding.DecodeString(text)
		if err != nil {
			state.fail(path, "encoding", "字节字段的 base64 编码无效")
		} else {
			target.SetBytes(decoded)
		}
		return
	}
	if plan.text {
		text, ok := raw.(string)
		if !ok {
			state.fail(path, "type", "字段必须为字符串")
			return
		}
		if err := target.Addr().Interface().(encoding.TextUnmarshaler).UnmarshalText([]byte(text)); err != nil {
			state.fail(path, "format", "字段格式无效")
		}
		return
	}
	switch target.Kind() {
	case reflect.Struct:
		object, ok := raw.(map[string]any)
		if !ok {
			state.fail(path, "type", "字段必须为对象")
			return
		}
		var data map[string]any
		if plan.validator != nil {
			data = make(map[string]any)
		}
		for _, item := range plan.fields {
			value, exists := object[item.name]
			if exists && item.quoted {
				var valid bool
				value, valid = state.unquote(value, path+"."+item.name)
				if !valid {
					continue
				}
			}
			if !exists && item.hasDefault {
				value, exists = item.defaultValue, true
			}
			if !exists {
				continue
			}
			if value != nil && data != nil {
				data[item.name] = value
			}
			state.assign(item.node, fieldValue(target, item.index), value, path+"."+item.name, depth+1)
		}
		unknownFields(state, object, plan.knownFields, path)
		state.validate(plan, data, func(item field) string { return path + "." + item.name })
	case reflect.Slice, reflect.Array:
		values, ok := raw.([]any)
		if !ok || target.Kind() == reflect.Array && len(values) != target.Len() {
			state.fail(path, "type", "数组类型或长度无效")
			return
		}
		if target.Kind() == reflect.Slice {
			target.Set(reflect.MakeSlice(target.Type(), len(values), len(values)))
		}
		for index, value := range values {
			state.assign(plan.elem, target.Index(index), value, fmt.Sprintf("%s[%d]", path, index), depth+1)
		}
	case reflect.Map:
		values, ok := raw.(map[string]any)
		if !ok {
			state.fail(path, "type", "字段必须为对象")
			return
		}
		target.Set(reflect.MakeMapWithSize(target.Type(), len(values)))
		keys := sortedKeys(values)
		for _, key := range keys {
			value := reflect.New(target.Type().Elem()).Elem()
			state.assign(plan.elem, value, values[key], path+"["+strconv.Quote(key)+"]", depth+1)
			target.SetMapIndex(reflect.ValueOf(key).Convert(target.Type().Key()), value)
		}
	default:
		if !assignScalar(target, raw) {
			state.fail(path, "type", "字段类型或数值范围无效")
		}
	}
}

func (state *bindState) validate(plan *node, data map[string]any, path func(field) string) {
	if plan.validator == nil {
		return
	}
	options := []validate.Option{validate.CollectAllErrors()}
	options = append(options, state.options...)
	result, err := plan.validator.Validate(data, options...)
	if err != nil {
		state.definitionErr = fmt.Errorf("%w: %v", ErrDefinition, err)
		return
	}
	for _, violation := range result.Violations() {
		for _, item := range plan.fields {
			if item.name == violation.Field {
				if len(state.issues) < maximumIssues {
					state.issues = append(state.issues, Issue{Field: path(item), Rule: violation.Rule, Message: violation.Message})
				}
				break
			}
		}
	}
}

// unknownFields 直接读取来源快照的键，避免为表单检查复制值；错误顺序仍由排序后的键确定。
func unknownFields[T any](state *bindState, object map[string]T, known map[string]bool, path string) {
	for _, key := range sortedKeys(object) {
		if !known[key] {
			state.fail(path+"."+key, "unknown", "未声明的请求字段")
		}
	}
}
func sortedKeys[T any](object map[string]T) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
func (state *bindState) fail(path, rule, message string) {
	state.malformed = true
	if len(state.issues) < maximumIssues {
		state.issues = append(state.issues, Issue{Field: path, Rule: rule, Message: message})
	}
}
func (state *bindState) err() error {
	if state.definitionErr != nil {
		return state.definitionErr
	}
	if len(state.issues) == 0 {
		return nil
	}
	status, message := http.StatusUnprocessableEntity, "请求验证失败"
	if state.malformed {
		status, message = http.StatusBadRequest, "请求参数无效"
	}
	return exception.NewHttpException(status, message).WithData(map[string]interface{}{"errors": append([]Issue(nil), state.issues...)})
}
