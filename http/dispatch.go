package http

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/binding"
	"github.com/zhuhanxin0308/thinkgo/v3/context"
	frameworkLog "github.com/zhuhanxin0308/thinkgo/v3/log"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
	"github.com/zhuhanxin0308/thinkgo/v3/route"
)

var (
	appPointerType          = reflect.TypeOf((*framework.App)(nil))
	requestPointerType      = reflect.TypeOf((*context.Request)(nil))
	baseControllerValueType = reflect.TypeOf(framework.Controller{})
)

type controllerDispatchPlan struct {
	controllerType     reflect.Type
	actionMethod       reflect.Method
	actionArguments    []controllerActionArgument
	actionVariadic     bool
	actionErrorOnly    bool
	actionReturnsError bool
	initializeMethod   *reflect.Method
}

type controllerActionArgumentKind uint8

const (
	controllerActionArgumentValue controllerActionArgumentKind = iota
	controllerActionArgumentApp
	controllerActionArgumentRequest
	controllerActionArgumentService
	controllerActionArgumentInput
)

type controllerActionArgument struct {
	kind          controllerActionArgumentKind
	parameterType reflect.Type
	variadic      bool
}

// dispatch 将已校验路由处理器分发到函数或控制器动作。
func (h *Http) dispatch(matchedRoute *route.Route, req *context.Request) *context.Response {
	if matchedRoute == nil {
		return h.dispatchInternalError("", errors.New("路由为空"))
	}
	handler := matchedRoute.Handler()
	if typed, ok := handler.(*route.JSONHandler); ok {
		return h.invokeRouteCallbackWithStatus(reflect.ValueOf(typed.Callback()), matchedRoute, req, typed.SuccessStatus())
	}
	if function, ok := handler.(func(*context.Request) *context.Response); ok {
		return function(req)
	}
	if standardHandler, ok := standardRouteHandler(handler); ok {
		writer, exists := req.ResponseWriter()
		if !exists {
			return h.dispatchInternalError("", errors.New("标准库路由处理器缺少响应写入器"))
		}
		standardHandler.ServeHTTP(writer, req.Raw())
		return context.NewCommittedResponse(responseWriterStatus(writer))
	}
	if callback := reflect.ValueOf(handler); callback.IsValid() && callback.Kind() == reflect.Func {
		return h.invokeRouteCallback(callback, matchedRoute, req)
	}
	handlerName, ok := handler.(string)
	if !ok {
		return h.dispatchInternalError("", fmt.Errorf("不支持的路由处理器类型 %T", handler))
	}
	controllerName, actionName, layerName, requestControllerName, requestActionName, validHandler := parseControllerHandler(handlerName)
	if !validHandler {
		return h.dispatchInternalError(handlerName, errors.New("控制器路由格式非法"))
	}
	if matchedRoute.IsAuto() && isReservedControllerMethod(actionName) {
		return context.NewResponse().Code(http.StatusNotFound).Content("404 Not Found")
	}
	// ThinkPHP 在实例化控制器前由 parseDispatch 写入调度元数据，
	// 因而 BaseController.Initialize 和控制器中间件都可以直接读取。
	req.SetLayer(layerName).SetController(requestControllerName).SetAction(requestActionName)
	resolvedControllerName, resolvedActionName := h.configuredControllerAction(controllerName, actionName)

	controllerInstance, err := h.resolveController(req, resolvedControllerName)
	if err != nil && matchedRoute.IsAuto() {
		// 控制器层对应 ThinkPHP 的 url_controller_layer；直接名称作为兼容回退。
		if layer := strings.TrimSpace(matchedRoute.ControllerLayer()); layer != "" {
			layeredName := layer + "." + resolvedControllerName
			if layeredInstance, layeredErr := h.resolveController(req, layeredName); layeredErr == nil {
				controllerInstance = layeredInstance
				err = nil
			}
		}
	}
	if err != nil {
		if matchedRoute.IsAuto() {
			return context.NewResponse().Code(http.StatusNotFound).Content("404 Not Found")
		}
		return h.dispatchInternalError(handlerName, err)
	}
	controllerValue := reflect.ValueOf(controllerInstance)
	if !controllerValue.IsValid() || isNilReflectValue(controllerValue) {
		return h.dispatchInternalError(handlerName, errors.New("控制器实例为空"))
	}
	// 自动路由的处理器名称和控制器类型同样稳定，复用反射计划可避免每次请求重复解析方法签名。
	plan, err := h.resolveDispatchPlan(handlerName, resolvedActionName, controllerValue.Type(), true)
	if err != nil {
		if matchedRoute.IsAuto() {
			return context.NewResponse().Code(http.StatusNotFound).Content("404 Not Found")
		}
		return h.dispatchInternalError(handlerName, err)
	}

	if err = injectControllerContext(controllerValue, h.app, req); err != nil {
		return h.dispatchInternalError(handlerName, err)
	}
	if plan.initializeMethod != nil {
		plan.initializeMethod.Func.Call([]reflect.Value{controllerValue})
	}
	handlers, err := h.resolveControllerMiddleware(controllerInstance, actionName)
	if err != nil {
		return h.dispatchInternalError(handlerName, err)
	}

	invokeAction := func(current *context.Request) *context.Response {
		return h.invokeControllerAction(handlerName, controllerValue, plan, matchedRoute, current)
	}
	if len(handlers) == 0 {
		return invokeAction(req)
	}
	return middleware.ThenHandlers(req, handlers, invokeAction)
}

// injectControllerContext 在内核内部完成 ThinkPHP BaseController 构造阶段的
// App、Request 注入。业务控制器只需要嵌入 Controller 并按需实现 Initialize。
func injectControllerContext(controller reflect.Value, app *framework.App, request *context.Request) error {
	_, err := injectEmbeddedControllerContext(controller, app, request, make(map[reflect.Type]bool))
	return err
}

func injectEmbeddedControllerContext(value reflect.Value, app *framework.App, request *context.Request, visiting map[reflect.Type]bool) (bool, error) {
	for value.IsValid() && (value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer) {
		if value.IsNil() {
			return false, nil
		}
		value = value.Elem()
	}
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return false, nil
	}
	if value.Type() == baseControllerValueType {
		if !value.CanAddr() || !value.Addr().CanInterface() {
			return false, errors.New("基础控制器不可写")
		}
		base, ok := value.Addr().Interface().(*framework.Controller)
		if !ok {
			return false, errors.New("基础控制器类型非法")
		}
		base.App = app
		base.Request = request
		return true, nil
	}
	if visiting[value.Type()] {
		return false, nil
	}
	visiting[value.Type()] = true
	defer delete(visiting, value.Type())

	typeOfValue := value.Type()
	for index := 0; index < value.NumField(); index++ {
		definition := typeOfValue.Field(index)
		if !definition.Anonymous || !typeEmbedsBaseController(definition.Type, make(map[reflect.Type]bool)) {
			continue
		}
		field := value.Field(index)
		if field.Kind() == reflect.Pointer && field.IsNil() {
			if !field.CanSet() {
				return false, fmt.Errorf("嵌入基础控制器字段 %s 不可写", definition.Name)
			}
			field.Set(reflect.New(field.Type().Elem()))
		}
		injected, err := injectEmbeddedControllerContext(field, app, request, visiting)
		if err != nil || injected {
			return injected, err
		}
	}
	return false, nil
}

func typeEmbedsBaseController(candidate reflect.Type, visiting map[reflect.Type]bool) bool {
	for candidate.Kind() == reflect.Pointer {
		candidate = candidate.Elem()
	}
	if candidate == baseControllerValueType {
		return true
	}
	if candidate.Kind() != reflect.Struct || visiting[candidate] {
		return false
	}
	visiting[candidate] = true
	defer delete(visiting, candidate)
	for index := 0; index < candidate.NumField(); index++ {
		field := candidate.Field(index)
		if field.Anonymous && typeEmbedsBaseController(field.Type, visiting) {
			return true
		}
	}
	return false
}

// configuredControllerAction 把 ThinkPHP 的 controller_suffix 和 action_suffix
// 统一应用到最终反射类型与方法名，业务路由仍使用 controller/action 写法。
func (h *Http) configuredControllerAction(controllerName, actionName string) (string, string) {
	if h == nil || h.app == nil || h.app.Config() == nil {
		return controllerName, actionName
	}
	if h.app.Config().GetBool("route.controller_suffix", false) {
		parts := strings.Split(controllerName, ".")
		last := len(parts) - 1
		if last >= 0 && !strings.HasSuffix(parts[last], "Controller") {
			parts[last] += "Controller"
		}
		controllerName = strings.Join(parts, ".")
	}
	actionSuffix := studlyRouteIdentifier(h.app.Config().GetString("route.action_suffix", ""))
	if actionSuffix != "" && !strings.HasSuffix(actionName, actionSuffix) {
		actionName += actionSuffix
	}
	return controllerName, actionName
}

// parseControllerHandler 同时支持 ThinkPHP 的 controller/action 写法和旧版
// Controller@Action 写法。前两个返回值供 Go 反射分发，后三个名称保持
// ThinkPHP parseDispatch 写入 Request 时的大小写和分层语义。
func parseControllerHandler(handlerName string) (string, string, string, string, string, bool) {
	handlerName = strings.TrimSpace(handlerName)
	if handlerName == "" {
		return "", "", "", "", "", false
	}
	if strings.Contains(handlerName, "@") {
		controllerName, actionName, found := strings.Cut(handlerName, "@")
		if !found || controllerName == "" || actionName == "" || strings.Contains(actionName, "@") {
			return "", "", "", "", "", false
		}
		controllerParts := strings.Split(controllerName, ".")
		layerName := ""
		if len(controllerParts) > 1 {
			layerName = strings.Join(controllerParts[:len(controllerParts)-1], ".")
		}
		return controllerName, actionName, layerName, controllerName, actionName, true
	}
	parts := strings.Split(strings.Trim(handlerName, "/"), "/")
	if len(parts) < 2 {
		return "", "", "", "", "", false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", "", "", "", "", false
		}
	}
	controllerParts := make([]string, 0, len(parts)-1)
	for _, part := range parts[:len(parts)-1] {
		name := studlyRouteIdentifier(part)
		if name == "" {
			return "", "", "", "", "", false
		}
		controllerParts = append(controllerParts, name)
	}
	actionName := studlyRouteIdentifier(parts[len(parts)-1])
	if actionName == "" {
		return "", "", "", "", "", false
	}
	layerName := ""
	if len(parts) > 2 {
		layerName = strings.Join(parts[:len(parts)-2], ".")
	}
	requestControllerName := controllerParts[len(controllerParts)-1]
	if layerName != "" {
		requestControllerName = layerName + "." + requestControllerName
	}
	return strings.Join(controllerParts, "."), actionName, layerName, requestControllerName, parts[len(parts)-1], true
}

func studlyRouteIdentifier(value string) string {
	segments := strings.FieldsFunc(value, func(character rune) bool {
		return character == '_' || character == '-'
	})
	if len(segments) == 0 {
		return ""
	}
	var result strings.Builder
	for _, segment := range segments {
		if segment == "" {
			return ""
		}
		runes := []rune(segment)
		if len(runes) == 0 {
			return ""
		}
		if runes[0] >= 'a' && runes[0] <= 'z' {
			runes[0] -= 'a' - 'A'
		}
		result.WriteString(string(runes))
	}
	return result.String()
}

func (h *Http) resolveController(req *context.Request, name string, params ...interface{}) (interface{}, error) {
	instance, err := req.Make(name, params...)
	if !errors.Is(err, context.ErrRequestServiceScopeUnavailable) {
		return instance, err
	}
	// dispatch 的包内测试与自定义内核可能直接构造 Request；保持该低层入口兼容，标准 HTTP 路径始终使用请求作用域。
	return h.app.MakeContext(req.Context(), name, params...)
}

func standardRouteHandler(handler route.HandlerFunc) (netHandler http.Handler, ok bool) {
	switch typed := handler.(type) {
	case http.Handler:
		return typed, true
	case func(http.ResponseWriter, *http.Request):
		return http.HandlerFunc(typed), true
	default:
		return nil, false
	}
}

func responseWriterStatus(writer http.ResponseWriter) int {
	if statusWriter, ok := writer.(interface{ Status() int }); ok {
		status := statusWriter.Status()
		if status >= http.StatusOK && status <= 599 {
			return status
		}
	}
	return http.StatusOK
}

func (h *Http) invokeControllerAction(handlerName string, controller reflect.Value, plan *controllerDispatchPlan, matchedRoute *route.Route, req *context.Request) *context.Response {
	arguments, err := h.bindControllerActionArguments(controller, plan, matchedRoute, req)
	if err != nil {
		return h.responseForRecoveredRequest(req, err)
	}
	compiled := binding.CallPlan{Variadic: plan.actionVariadic, ErrorOnly: plan.actionErrorOnly, ReturnsError: plan.actionReturnsError}
	return h.invokeActionCall(plan.actionMethod.Func, arguments, compiled, req, 0, handlerName)
}

// bindControllerActionArguments 按 ThinkPHP action_bind_param 规则绑定动作参数。
// Go 运行时不保留形参名称，因此显式路由优先使用路由变量声明顺序；自动路由
// 在没有路由变量时使用稳定排序后的请求参数名。
func (h *Http) bindControllerActionArguments(controller reflect.Value, plan *controllerDispatchPlan, matchedRoute *route.Route, req *context.Request) ([]reflect.Value, error) {
	if plan == nil {
		return nil, errors.New("控制器动作计划为空")
	}
	return h.bindActionArguments([]reflect.Value{controller}, plan.actionArguments, matchedRoute, req)
}

// bindActionArguments 共用控制器与回调的请求取值、依赖解析和 Optional 绑定行为。
func (h *Http) bindActionArguments(prefix []reflect.Value, planned []controllerActionArgument, matchedRoute *route.Route, req *context.Request) ([]reflect.Value, error) {
	arguments := make([]reflect.Value, len(prefix), len(prefix)+len(planned))
	copy(arguments, prefix)
	parameterNames := h.controllerActionParameterNames(&controllerDispatchPlan{actionArguments: planned}, matchedRoute, req)
	valueIndex := 0
	for _, argument := range planned {
		switch argument.kind {
		case controllerActionArgumentApp:
			if h == nil || h.app == nil {
				return nil, framework.ErrNilApplication
			}
			arguments = append(arguments, reflect.ValueOf(h.app))
		case controllerActionArgumentRequest:
			if req == nil {
				return nil, errors.New("控制器动作请求为空")
			}
			arguments = append(arguments, reflect.ValueOf(req))
		case controllerActionArgumentService:
			dependency, err := h.resolveControllerActionService(req, argument.parameterType)
			if err != nil {
				return nil, err
			}
			arguments = append(arguments, dependency)
		case controllerActionArgumentInput:
			input, err := h.bindInputArgument(req, argument.parameterType)
			if err != nil {
				return nil, err
			}
			arguments = append(arguments, input)
		case controllerActionArgumentValue:
			if argument.variadic {
				values := reflect.MakeSlice(argument.parameterType, 0, len(parameterNames)-valueIndex)
				for valueIndex < len(parameterNames) {
					name := parameterNames[valueIndex]
					valueIndex++
					raw, exists := h.controllerActionSourceValue(req, name)
					if !exists {
						continue
					}
					converted, err := convertControllerActionValue(raw, argument.parameterType.Elem())
					if err != nil {
						return nil, actionParameterError(name, "type", "参数类型无效")
					}
					values = reflect.Append(values, converted)
				}
				arguments = append(arguments, values)
				continue
			}
			if valueIndex >= len(parameterNames) {
				return nil, actionParameterError("", "required", "缺少动作参数")
			}
			name := parameterNames[valueIndex]
			valueIndex++
			raw, exists := h.controllerActionSourceValue(req, name)
			if !exists {
				if argument.parameterType.Kind() == reflect.Pointer {
					arguments = append(arguments, reflect.Zero(argument.parameterType))
					continue
				}
				return nil, actionParameterError(name, "required", "缺少动作参数")
			}
			converted, err := convertControllerActionValue(raw, argument.parameterType)
			if err != nil {
				return nil, actionParameterError(name, "type", "参数类型无效")
			}
			arguments = append(arguments, converted)
		default:
			return nil, fmt.Errorf("不支持的动作参数类型 %s", argument.parameterType)
		}
	}
	return arguments, nil
}

func (h *Http) controllerActionParameterNames(plan *controllerDispatchPlan, matchedRoute *route.Route, req *context.Request) []string {
	names := make([]string, 0)
	if matchedRoute != nil {
		names = append(names, matchedRoute.ParameterNames()...)
	}
	requiredValues := 0
	hasVariadic := false
	for _, argument := range plan.actionArguments {
		if argument.kind != controllerActionArgumentValue {
			continue
		}
		if argument.variadic {
			hasVariadic = true
			continue
		}
		requiredValues++
	}
	if len(names) >= requiredValues && !(len(names) == 0 && hasVariadic) {
		return names
	}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		seen[name] = true
	}
	for _, name := range h.availableControllerActionParameterNames(req) {
		if !seen[name] {
			names = append(names, name)
			seen[name] = true
		}
	}
	return names
}

func (h *Http) availableControllerActionParameterNames(req *context.Request) []string {
	if req == nil {
		return nil
	}
	mode := h.controllerActionBindMode()
	if mode == "route" {
		return nil
	}
	keys := make([]string, 0)
	if mode == "param" {
		for key := range req.All() {
			keys = append(keys, key)
		}
	} else {
		query, _ := req.QueryValues()
		for key := range query {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func (h *Http) controllerActionSourceValue(req *context.Request, name string) (interface{}, bool) {
	if req == nil {
		return nil, false
	}
	mode := h.controllerActionBindMode()
	if value, exists := req.RouteValue(name); exists {
		return value, true
	}
	if mode == "route" {
		return nil, false
	}
	if mode == "param" {
		value, exists := req.All()[name]
		return value, exists
	}
	query, err := req.QueryValues()
	if err != nil {
		return nil, false
	}
	values, exists := query[name]
	if !exists || len(values) == 0 {
		return nil, false
	}
	if len(values) == 1 {
		return values[0], true
	}
	return append([]string(nil), values...), true
}

func (h *Http) controllerActionBindMode() string {
	if h == nil || h.app == nil || h.app.Config() == nil {
		return "get"
	}
	mode := strings.ToLower(strings.TrimSpace(h.app.Config().GetString("route.action_bind_param", "get")))
	switch mode {
	case "route", "param":
		return mode
	default:
		return "get"
	}
}

func (h *Http) resolveControllerActionService(req *context.Request, parameterType reflect.Type) (reflect.Value, error) {
	if h.app.HasModelType(parameterType) {
		return h.resolveActionModel(req, parameterType)
	}
	candidates := make([]string, 0, 3)
	if parameterType != nil {
		candidates = append(candidates, parameterType.String())
		if parameterType.Kind() == reflect.Pointer {
			candidates = append(candidates, parameterType.Elem().Name())
		} else if parameterType.Name() != "" {
			candidates = append(candidates, parameterType.Name())
		}
	}
	seen := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || seen[candidate] {
			continue
		}
		seen[candidate] = true
		service, err := h.resolveController(req, candidate)
		if err != nil || service == nil {
			continue
		}
		value := reflect.ValueOf(service)
		if value.IsValid() && value.Type().AssignableTo(parameterType) {
			return value, nil
		}
	}
	return reflect.Value{}, fmt.Errorf("无法从容器解析动作依赖 %s", parameterType)
}

func convertControllerActionValue(raw interface{}, target reflect.Type) (reflect.Value, error) {
	if target == nil {
		return reflect.Value{}, errors.New("目标参数类型为空")
	}
	if raw == nil {
		if target.Kind() == reflect.Pointer || target.Kind() == reflect.Interface || target.Kind() == reflect.Slice {
			return reflect.Zero(target), nil
		}
		return reflect.Value{}, errors.New("参数值不能为空")
	}
	rawValue := reflect.ValueOf(raw)
	if rawValue.IsValid() && rawValue.Type().AssignableTo(target) {
		return rawValue, nil
	}
	if target.Kind() == reflect.Interface && rawValue.IsValid() && rawValue.Type().Implements(target) {
		return rawValue, nil
	}
	if target.Kind() == reflect.Pointer {
		converted, err := convertControllerActionValue(raw, target.Elem())
		if err != nil {
			return reflect.Value{}, err
		}
		pointer := reflect.New(target.Elem())
		pointer.Elem().Set(converted)
		return pointer, nil
	}
	if target.Kind() == reflect.Slice {
		items := make([]interface{}, 0)
		source := reflect.ValueOf(raw)
		if source.IsValid() && (source.Kind() == reflect.Slice || source.Kind() == reflect.Array) {
			for index := 0; index < source.Len(); index++ {
				items = append(items, source.Index(index).Interface())
			}
		} else {
			items = append(items, raw)
		}
		converted := reflect.MakeSlice(target, 0, len(items))
		for _, item := range items {
			value, err := convertControllerActionValue(item, target.Elem())
			if err != nil {
				return reflect.Value{}, err
			}
			converted = reflect.Append(converted, value)
		}
		return converted, nil
	}
	text, err := controllerActionScalarText(raw)
	if err != nil {
		return reflect.Value{}, err
	}
	converted := reflect.New(target).Elem()
	switch target.Kind() {
	case reflect.String:
		converted.SetString(text)
	case reflect.Bool:
		value, parseErr := strconv.ParseBool(text)
		if parseErr != nil {
			return reflect.Value{}, parseErr
		}
		converted.SetBool(value)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		value, parseErr := strconv.ParseInt(text, 10, target.Bits())
		if parseErr != nil {
			return reflect.Value{}, parseErr
		}
		converted.SetInt(value)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		value, parseErr := strconv.ParseUint(text, 10, target.Bits())
		if parseErr != nil {
			return reflect.Value{}, parseErr
		}
		converted.SetUint(value)
	case reflect.Float32, reflect.Float64:
		value, parseErr := strconv.ParseFloat(text, target.Bits())
		if parseErr != nil {
			return reflect.Value{}, parseErr
		}
		converted.SetFloat(value)
	default:
		return reflect.Value{}, fmt.Errorf("不支持把标量转换为 %s", target)
	}
	return converted, nil
}

func controllerActionScalarText(raw interface{}) (string, error) {
	value := reflect.ValueOf(raw)
	if !value.IsValid() {
		return "", errors.New("参数值无效")
	}
	for value.Kind() == reflect.Interface {
		if value.IsNil() {
			return "", errors.New("参数值不能为空")
		}
		value = value.Elem()
	}
	switch value.Kind() {
	case reflect.String:
		return value.String(), nil
	case reflect.Bool:
		return strconv.FormatBool(value.Bool()), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(value.Int(), 10), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(value.Uint(), 10), nil
	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(value.Float(), 'g', -1, value.Type().Bits()), nil
	default:
		return "", fmt.Errorf("参数值类型 %T 不是可绑定标量", raw)
	}
}

func (h *Http) resolveDispatchPlan(handlerName, actionName string, controllerType reflect.Type, cacheable bool) (*controllerDispatchPlan, error) {
	if cacheable {
		h.dispatchPlanMu.RLock()
		cached := h.dispatchPlans[handlerName]
		h.dispatchPlanMu.RUnlock()
		if cached != nil && cached.controllerType == controllerType {
			return cached, nil
		}
	}

	if actionName == "" || strings.Contains(actionName, "@") {
		return nil, errors.New("控制器路由格式非法")
	}
	actionMethod, exists := controllerType.MethodByName(actionName)
	if !exists {
		return nil, fmt.Errorf("控制器方法不存在: %s", actionName)
	}
	plan := &controllerDispatchPlan{controllerType: controllerType, actionMethod: actionMethod}
	if err := validateActionSignature(plan); err != nil {
		return nil, fmt.Errorf("控制器动作 %s 签名非法: %w", actionName, err)
	}
	if initializeMethod, ok := controllerType.MethodByName("Initialize"); ok {
		copied := initializeMethod
		plan.initializeMethod = &copied
		if err := validateInitializeSignature(plan); err != nil {
			return nil, fmt.Errorf("控制器 Initialize 签名非法: %w", err)
		}
	}

	if cacheable {
		h.dispatchPlanMu.Lock()
		if existing := h.dispatchPlans[handlerName]; existing != nil && existing.controllerType == controllerType {
			plan = existing
		} else {
			h.dispatchPlans[handlerName] = plan
		}
		h.dispatchPlanMu.Unlock()
	}
	return plan, nil
}

func validateActionSignature(plan *controllerDispatchPlan) error {
	compiled, arguments, err := compileActionCall(plan.actionMethod.Type, true)
	if err != nil {
		return err
	}
	plan.actionArguments = arguments
	plan.actionVariadic, plan.actionErrorOnly, plan.actionReturnsError = compiled.Variadic, compiled.ErrorOnly, compiled.ReturnsError
	return nil
}

func validateInitializeSignature(plan *controllerDispatchPlan) error {
	methodType := plan.initializeMethod.Type
	if methodType.IsVariadic() || methodType.NumIn() != 1 {
		return errors.New("不能接收参数")
	}
	if methodType.NumOut() != 0 {
		return errors.New("不能声明返回值")
	}
	return nil
}

type controllerMiddlewareProvider interface {
	GetMiddleware() []framework.ControllerMiddleware
}

func (h *Http) resolveControllerMiddleware(controllerInstance interface{}, action string) ([]middleware.Handler, error) {
	provider, ok := controllerInstance.(controllerMiddlewareProvider)
	if !ok {
		return nil, nil
	}
	declarations := provider.GetMiddleware()
	if len(declarations) == 0 {
		return nil, nil
	}
	handlers := make([]middleware.Handler, 0, len(declarations))
	for index, declaration := range declarations {
		declaration.Name = strings.TrimSpace(declaration.Name)
		if declaration.Name == "" {
			return nil, fmt.Errorf("第 %d 条控制器中间件名称为空", index+1)
		}
		if len(declaration.Only) > 0 && len(declaration.Except) > 0 {
			return nil, fmt.Errorf("控制器中间件 %q 不能同时配置 Only 和 Except", declaration.Name)
		}
		if err := validateControllerActionFilter(declaration.Only); err != nil {
			return nil, fmt.Errorf("控制器中间件 %q 的 Only 非法: %w", declaration.Name, err)
		}
		if err := validateControllerActionFilter(declaration.Except); err != nil {
			return nil, fmt.Errorf("控制器中间件 %q 的 Except 非法: %w", declaration.Name, err)
		}
		if !controllerMiddlewareApplies(declaration, action) {
			continue
		}
		handler := h.middleware.ResolveAlias(declaration.Name)
		if handler == nil {
			return nil, fmt.Errorf("控制器中间件别名 %q 未注册", declaration.Name)
		}
		handlers = append(handlers, handler)
	}
	return handlers, nil
}

func validateControllerActionFilter(actions []string) error {
	if len(actions) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(actions))
	for _, action := range actions {
		action = strings.TrimSpace(action)
		if action == "" {
			return errors.New("动作名不能为空")
		}
		for index, char := range action {
			letter := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z'
			digit := char >= '0' && char <= '9'
			if index == 0 && !letter && char != '_' {
				return fmt.Errorf("动作名 %q 非法", action)
			}
			if index > 0 && !letter && !digit && char != '_' {
				return fmt.Errorf("动作名 %q 非法", action)
			}
		}
		key := strings.ToLower(action)
		if seen[key] {
			return fmt.Errorf("动作名 %q 重复", action)
		}
		seen[key] = true
	}
	return nil
}

func controllerMiddlewareApplies(declaration framework.ControllerMiddleware, action string) bool {
	if len(declaration.Only) > 0 {
		return containsFold(declaration.Only, action)
	}
	if len(declaration.Except) > 0 {
		return !containsFold(declaration.Except, action)
	}
	return true
}

func containsFold(items []string, target string) bool {
	for _, item := range items {
		if strings.EqualFold(strings.TrimSpace(item), target) {
			return true
		}
	}
	return false
}

var reservedControllerMethods = buildReservedControllerMethods()

func buildReservedControllerMethods() map[string]bool {
	set := make(map[string]bool)
	for _, controllerType := range []reflect.Type{
		reflect.TypeOf(framework.Controller{}),
		reflect.TypeOf(&framework.Controller{}),
	} {
		for index := 0; index < controllerType.NumMethod(); index++ {
			set[controllerType.Method(index).Name] = true
		}
	}
	return set
}

func isReservedControllerMethod(name string) bool {
	return reservedControllerMethods[name]
}

func (h *Http) toResponse(result reflect.Value) (*context.Response, error) {
	if !result.IsValid() || isNilReflectValue(result) {
		return context.NewResponse(), nil
	}
	value := result.Interface()
	switch typed := value.(type) {
	case *context.Response:
		if typed == nil {
			return nil, errors.New("控制器返回了空 Response")
		}
		return typed, nil
	case context.Response:
		return &typed, nil
	case string:
		return context.NewResponse().Content(typed), nil
	default:
		return context.NewResponse().Json(value), nil
	}
}

func reflectResultError(value reflect.Value) error {
	if !value.IsValid() || isNilReflectValue(value) {
		return nil
	}
	result, _ := value.Interface().(error)
	return result
}

func isNilReflectValue(value reflect.Value) bool {
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (h *Http) dispatchInternalError(handler string, err error) *context.Response {
	if h.log != nil && err != nil {
		h.log.ErrorCtx("HTTP 控制器分发失败", map[string]interface{}{
			"handler": handler,
			"error":   frameworkLog.SanitizeErrorText(err.Error()),
		})
	}
	message := http.StatusText(http.StatusInternalServerError)
	if h.app != nil && h.app.IsDebug() && err != nil {
		message = err.Error()
	}
	return context.NewResponse().Code(http.StatusInternalServerError).Content(message)
}
