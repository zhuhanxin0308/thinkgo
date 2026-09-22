package binding

import (
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"

	fwcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
	"github.com/zhuhanxin0308/thinkgo/framework/validate"
	"golang.org/x/net/http/httpguts"
)

const (
	maximumDepth  = 64
	maximumFields = 1024
)

var (
	ErrDefinition            = errors.New("请求类型定义无效")
	optionalInterface        = reflect.TypeOf((*optionalValue)(nil)).Elem()
	textUnmarshalerInterface = reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()
	plans                    sync.Map
)

type planKey struct {
	typ  reflect.Type
	root bool
}
type node struct {
	typ         reflect.Type
	elem        *node
	fields      []field
	knownFields map[string]bool
	validator   *validate.Validator
	sources     fwcontext.SourceSelection
	hasBody     bool
	hasForm     bool
	optional    bool
	text        bool
	binary      bool
	number      bool
	rawJSON     bool
}
type field struct {
	index                            []int
	owner                            reflect.Type
	goName                           string
	name, source, rules, description string
	rootPath                         string
	node                             *node
	defaultValue                     any
	hasDefault                       bool
	omitEmpty                        bool
	quoted                           bool
	optionalParents                  []string
}
type compiledPlan struct {
	root *node
	err  error
}

// ValidateType 预编译请求结构，在启动阶段发现字段冲突和非法规则。
func ValidateType(typ reflect.Type) error {
	_, err := planFor(typ)
	return err
}

func planFor(typ reflect.Type) (*node, error) {
	if typ == nil {
		return nil, fmt.Errorf("%w: 类型为空", ErrDefinition)
	}
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return nil, fmt.Errorf("%w: 请求必须是结构体", ErrDefinition)
	}
	if cached, ok := plans.Load(typ); ok {
		result := cached.(compiledPlan)
		return result.root, result.err
	}
	result, err := compileNode(typ, true, false, make(map[planKey]*node), 0)
	if err == nil && (result.text || result.optional) {
		err = fmt.Errorf("%w: 请求根类型必须描述对象字段", ErrDefinition)
	}
	actual, _ := plans.LoadOrStore(typ, compiledPlan{result, err})
	stored := actual.(compiledPlan)
	return stored.root, stored.err
}

func compileNode(typ reflect.Type, root, output bool, building map[planKey]*node, depth int) (*node, error) {
	key := planKey{typ, root}
	if cached := building[key]; cached != nil {
		return cached, nil
	}
	if depth > maximumDepth {
		return nil, fmt.Errorf("%w: 类型嵌套过深", ErrDefinition)
	}
	result := &node{typ: typ}
	building[key] = result
	if typ == reflect.TypeFor[json.Number]() {
		result.number = true
		return result, nil
	}
	if typ == reflect.TypeFor[json.RawMessage]() {
		result.rawJSON = true
		return result, nil
	}
	if typ.Kind() != reflect.Pointer && reflect.PointerTo(typ).Implements(optionalInterface) {
		result.optional = true
		holder := reflect.New(typ).Interface().(optionalValue)
		element := reflect.TypeOf(holder.valuePointer()).Elem()
		var err error
		result.elem, err = compileNode(element, false, output, building, depth+1)
		return result, err
	}
	if typ.Kind() != reflect.Pointer && (typ.PkgPath() != "time" || typ.Name() != "Time") {
		pointer := reflect.PointerTo(typ)
		if pointer.Implements(reflect.TypeFor[json.Marshaler]()) || pointer.Implements(reflect.TypeFor[json.Unmarshaler]()) {
			return nil, fmt.Errorf("%w: 自定义 JSON 类型 %s 无法自动推导字段契约", ErrDefinition, typ)
		}
	}
	if typ.Kind() != reflect.Pointer && reflect.PointerTo(typ).Implements(textUnmarshalerInterface) {
		result.text = true
		return result, nil
	}
	if typ.Kind() == reflect.Slice && typ.Elem().Kind() == reflect.Uint8 {
		result.binary = true
		return result, nil
	}
	switch typ.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
		if typ.Kind() == reflect.Map && typ.Key().Kind() != reflect.String {
			return nil, fmt.Errorf("%w: map 键必须是字符串", ErrDefinition)
		}
		var err error
		result.elem, err = compileNode(typ.Elem(), root && typ.Kind() == reflect.Pointer, output, building, depth+1)
		return result, err
	case reflect.Struct:
		fields, err := compileFields(typ, root, output, building, depth, nil, make(map[reflect.Type]bool))
		if err != nil {
			return nil, err
		}
		result.fields = fields
		if len(fields) > maximumFields {
			return nil, fmt.Errorf("%w: 字段过多", ErrDefinition)
		}
		rules, names := make(map[string]string), make(map[string]bool)
		// 字段声明在计划发布后保持只读；仅请求体和表单字段进入来源白名单。
		result.knownFields = make(map[string]bool, len(fields))
		for _, item := range fields {
			result.hasBody = result.hasBody || item.source == "body"
			result.hasForm = result.hasForm || item.source == "form"
			if root {
				// 来源选择与字段计划一同发布，不在每次绑定时重复扫描声明。
				switch item.source {
				case "body":
					result.sources.Body = true
				case "form":
					result.sources.Form = true
				case "query":
					result.sources.Query = true
				case "header":
					result.sources.Header = true
				case "cookie":
					result.sources.Cookies = true
				}
			}
			if item.source == "body" || item.source == "form" {
				result.knownFields[item.name] = true
			}
			if names[item.name] {
				return nil, fmt.Errorf("%w: 字段名 %q 重复", ErrDefinition, item.name)
			}
			names[item.name] = true
			if item.rules != "" {
				rules[item.name] = item.rules
			}
		}
		if result.hasBody && result.hasForm {
			return nil, fmt.Errorf("%w: JSON 和表单来源不能混用", ErrDefinition)
		}
		if len(rules) > 0 {
			result.validator = validate.NewValidator().SetRules(rules)
			if _, err := result.validator.Validate(nil, validate.CollectAllErrors()); err != nil {
				return nil, fmt.Errorf("%w: %v", ErrDefinition, err)
			}
		}
	case reflect.String, reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
	case reflect.Interface:
		// 仅输出允许任意 JSON 值，例如驱动决定类型的分页游标；请求仍要求明确类型。
		if !output || typ.NumMethod() != 0 {
			return nil, fmt.Errorf("%w: 不支持类型 %s", ErrDefinition, typ)
		}
	default:
		return nil, fmt.Errorf("%w: 不支持类型 %s", ErrDefinition, typ)
	}
	return result, nil
}

func compileFields(typ reflect.Type, root, output bool, building map[planKey]*node, depth int, prefix []int, embedded map[reflect.Type]bool) ([]field, error) {
	if embedded[typ] {
		return nil, fmt.Errorf("%w: 匿名字段形成循环", ErrDefinition)
	}
	embedded[typ] = true
	defer delete(embedded, typ)
	var result []field
	for index := 0; index < typ.NumField(); index++ {
		spec := typ.Field(index)
		if spec.Anonymous && (spec.Type == reflect.TypeFor[Input]() || spec.Type == reflect.TypeFor[*Input]()) {
			continue
		}
		jsonName := strings.Split(spec.Tag.Get("json"), ",")[0]
		if jsonName == "-" {
			hasSource := false
			for _, source := range []string{"path", "query", "header", "cookie", "form"} {
				if _, exists := spec.Tag.Lookup(source); exists {
					hasSource = true
					break
				}
			}
			if !hasSource {
				continue
			}
			jsonName = ""
		}
		if spec.PkgPath != "" {
			if spec.Anonymous {
				return nil, fmt.Errorf("%w: 匿名字段 %s 未导出", ErrDefinition, spec.Name)
			}
			for _, tag := range []string{"json", "validate", "path", "query", "header", "cookie", "form", "default"} {
				if _, exists := spec.Tag.Lookup(tag); exists {
					return nil, fmt.Errorf("%w: 字段 %s 未导出", ErrDefinition, spec.Name)
				}
			}
			continue
		}
		indices := append(append([]int(nil), prefix...), index)
		source, name := "body", jsonName
		if name == "" {
			name = spec.Name
		}
		declared := 0
		for _, candidate := range []string{"path", "query", "header", "cookie", "form"} {
			if value, exists := spec.Tag.Lookup(candidate); exists {
				if !root || value == "" || value == "-" {
					return nil, fmt.Errorf("%w: 字段 %s 来源无效", ErrDefinition, spec.Name)
				}
				source, name = candidate, value
				declared++
			}
		}
		if declared > 1 {
			return nil, fmt.Errorf("%w: 字段 %s 声明多个来源", ErrDefinition, spec.Name)
		}
		if source == "header" {
			if !httpguts.ValidHeaderFieldName(name) {
				return nil, fmt.Errorf("%w: 请求头名称无效", ErrDefinition)
			}
			name = http.CanonicalHeaderKey(name)
		}
		element := spec.Type
		if element.Kind() == reflect.Pointer {
			element = element.Elem()
		}
		if spec.Anonymous && jsonName == "" && declared == 0 && element.Kind() == reflect.Struct {
			children, err := compileFields(element, root, output, building, depth+1, indices, embedded)
			if err != nil {
				return nil, err
			}
			if output && spec.Type.Kind() == reflect.Pointer {
				// 匿名指针为空时，JSON 编码会省略全部提升字段；保留各层父节点以描述成组出现约束。
				parent := fmt.Sprint(indices)
				for index := range children {
					children[index].optionalParents = append([]string{parent}, children[index].optionalParents...)
				}
			}
			result = append(result, children...)
			continue
		}
		child, err := compileNode(spec.Type, false, output, building, depth+1)
		if err != nil {
			return nil, fmt.Errorf("字段 %s: %w", spec.Name, err)
		}
		if source != "body" && !validParameterNode(child) {
			return nil, fmt.Errorf("%w: 参数 %s 必须是标量或一维标量数组", ErrDefinition, name)
		}
		item := field{index: indices, owner: typ, goName: spec.Name, name: name, source: source, node: child, rules: spec.Tag.Get("validate"), description: spec.Tag.Get("doc")}
		if root {
			// 根字段的来源和名称在编译时固定；匿名提升字段沿用来源路径，不附加 Go 字段层级。
			item.rootPath = source + "." + name
		}
		for _, option := range strings.Split(spec.Tag.Get("json"), ",")[1:] {
			item.omitEmpty = item.omitEmpty || option == "omitempty" || option == "omitzero"
			if option == "string" && source == "body" {
				kind := underlyingNode(child).typ.Kind()
				item.quoted = !child.optional && !child.text && (kind == reflect.String || kind >= reflect.Bool && kind <= reflect.Float64)
			}
		}
		if raw, exists := spec.Tag.Lookup("default"); exists {
			item.hasDefault = true
			defaultType := spec.Type
			if child.optional {
				defaultType = child.elem.typ
			}
			for defaultType.Kind() == reflect.Pointer {
				defaultType = defaultType.Elem()
			}
			encoded := raw
			if defaultType.Kind() == reflect.String && !underlyingNode(child).number || underlyingNode(child).text || underlyingNode(child).binary {
				encoded = strconv.Quote(raw)
			}
			if !json.Valid([]byte(encoded)) {
				return nil, fmt.Errorf("%w: 字段 %s 默认值不是单一 JSON 值", ErrDefinition, spec.Name)
			}
			decoder := json.NewDecoder(strings.NewReader(encoded))
			decoder.UseNumber()
			if err := decoder.Decode(&item.defaultValue); err != nil {
				return nil, fmt.Errorf("%w: 字段 %s 默认值无效", ErrDefinition, spec.Name)
			}
			probe := reflect.New(spec.Type).Elem()
			state := bindState{}
			state.assign(child, probe, item.defaultValue, item.name, 0)
			if len(state.issues) > 0 {
				return nil, fmt.Errorf("%w: 字段 %s 默认值类型无效", ErrDefinition, spec.Name)
			}
			if item.rules != "" {
				validation, err := validate.ValidateRules(map[string]interface{}{item.name: item.defaultValue}, map[string]string{item.name: item.rules})
				if err != nil || !validation.Valid() {
					return nil, fmt.Errorf("%w: 字段 %s 默认值不符合规则", ErrDefinition, spec.Name)
				}
			}
		}
		result = append(result, item)
		if len(result) > maximumFields {
			return nil, fmt.Errorf("%w: 字段过多", ErrDefinition)
		}
	}
	return result, nil
}

func validParameterNode(plan *node) bool {
	plan = underlyingNode(plan)
	if plan.rawJSON {
		return false
	}
	if plan.binary {
		return true
	}
	if plan.typ.Kind() == reflect.Array || plan.typ.Kind() == reflect.Slice {
		plan = underlyingNode(plan.elem)
	}
	return plan.text || plan.typ.Kind() >= reflect.Bool && plan.typ.Kind() <= reflect.Float64 || plan.typ.Kind() == reflect.String
}
