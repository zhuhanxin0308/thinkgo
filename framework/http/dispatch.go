package http

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"

	"thinkgo/framework"
	"thinkgo/framework/context"
	"thinkgo/framework/middleware"
	"thinkgo/framework/route"
)

var (
	appPointerType     = reflect.TypeOf((*framework.App)(nil))
	requestPointerType = reflect.TypeOf((*context.Request)(nil))
	errorInterfaceType = reflect.TypeOf((*error)(nil)).Elem()
)

type controllerDispatchPlan struct {
	controllerType     reflect.Type
	actionMethod       reflect.Method
	actionTakesRequest bool
	actionErrorOnly    bool
	actionReturnsError bool
	initMethod         *reflect.Method
	initReturnsError   bool
}

// dispatch 将已校验路由处理器分发到函数或控制器动作。
func (h *Http) dispatch(matchedRoute *route.Route, req *context.Request) *context.Response {
	if matchedRoute == nil {
		return h.dispatchInternalError("", errors.New("路由为空"))
	}
	handler := matchedRoute.Handler()
	if function, ok := handler.(func(*context.Request) *context.Response); ok {
		return function(req)
	}
	handlerName, ok := handler.(string)
	if !ok {
		return h.dispatchInternalError("", fmt.Errorf("不支持的路由处理器类型 %T", handler))
	}
	parts := strings.Split(handlerName, "@")
	if len(parts) != 2 {
		return h.dispatchInternalError(handlerName, errors.New("控制器路由格式非法"))
	}
	if matchedRoute.IsAuto() && isReservedControllerMethod(parts[1]) {
		return context.NewResponse().Code(http.StatusNotFound).Content("404 Not Found")
	}

	controllerInstance, err := h.app.Make(parts[0])
	if err != nil {
		return h.dispatchInternalError(handlerName, err)
	}
	controllerValue := reflect.ValueOf(controllerInstance)
	if !controllerValue.IsValid() || isNilReflectValue(controllerValue) {
		return h.dispatchInternalError(handlerName, errors.New("控制器实例为空"))
	}
	plan, err := h.resolveDispatchPlan(handlerName, controllerValue.Type(), !matchedRoute.IsAuto())
	if err != nil {
		return h.dispatchInternalError(handlerName, err)
	}

	if plan.initMethod != nil {
		results := plan.initMethod.Func.Call([]reflect.Value{controllerValue, reflect.ValueOf(h.app), reflect.ValueOf(req)})
		if plan.initReturnsError {
			if initErr := reflectResultError(results[0]); initErr != nil {
				return h.dispatchInternalError(handlerName, fmt.Errorf("控制器初始化失败: %w", initErr))
			}
		}
	}
	handlers, err := h.resolveControllerMiddleware(controllerInstance, parts[1])
	if err != nil {
		return h.dispatchInternalError(handlerName, err)
	}

	invokeAction := func(current *context.Request) *context.Response {
		return h.invokeControllerAction(handlerName, controllerValue, plan, current)
	}
	if len(handlers) == 0 {
		return invokeAction(req)
	}
	controllerPipeline := middleware.NewPipeline()
	for _, handler := range handlers {
		controllerPipeline.Pipe(handler)
	}
	return controllerPipeline.Then(req, invokeAction)
}

func (h *Http) invokeControllerAction(handlerName string, controller reflect.Value, plan *controllerDispatchPlan, req *context.Request) *context.Response {
	arguments := []reflect.Value{controller}
	if plan.actionTakesRequest {
		arguments = append(arguments, reflect.ValueOf(req))
	}
	results := plan.actionMethod.Func.Call(arguments)
	if plan.actionErrorOnly {
		if actionErr := reflectResultError(results[0]); actionErr != nil {
			return h.dispatchInternalError(handlerName, actionErr)
		}
		return context.NewResponse()
	}
	if plan.actionReturnsError {
		if actionErr := reflectResultError(results[len(results)-1]); actionErr != nil {
			return h.dispatchInternalError(handlerName, actionErr)
		}
		results = results[:len(results)-1]
	}
	if len(results) == 0 {
		return context.NewResponse()
	}
	response, err := h.toResponse(results[0])
	if err != nil {
		return h.dispatchInternalError(handlerName, err)
	}
	return response
}

func (h *Http) resolveDispatchPlan(handlerName string, controllerType reflect.Type, cacheable bool) (*controllerDispatchPlan, error) {
	if cacheable {
		h.dispatchPlanMu.RLock()
		cached := h.dispatchPlans[handlerName]
		h.dispatchPlanMu.RUnlock()
		if cached != nil && cached.controllerType == controllerType {
			return cached, nil
		}
	}

	parts := strings.Split(handlerName, "@")
	if len(parts) != 2 {
		return nil, errors.New("控制器路由格式非法")
	}
	actionMethod, exists := controllerType.MethodByName(parts[1])
	if !exists {
		return nil, fmt.Errorf("控制器方法不存在: %s", parts[1])
	}
	plan := &controllerDispatchPlan{controllerType: controllerType, actionMethod: actionMethod}
	if err := validateActionSignature(plan); err != nil {
		return nil, fmt.Errorf("控制器动作 %s 签名非法: %w", parts[1], err)
	}
	if initMethod, ok := controllerType.MethodByName("Init"); ok {
		copied := initMethod
		plan.initMethod = &copied
		if err := validateInitSignature(plan); err != nil {
			return nil, fmt.Errorf("控制器 Init 签名非法: %w", err)
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
	methodType := plan.actionMethod.Type
	if methodType.IsVariadic() || methodType.NumIn() < 1 || methodType.NumIn() > 2 {
		return errors.New("只允许零个参数或一个 *context.Request 参数")
	}
	if methodType.NumIn() == 2 {
		if !requestPointerType.AssignableTo(methodType.In(1)) {
			return fmt.Errorf("参数类型必须接收 %s", requestPointerType)
		}
		plan.actionTakesRequest = true
	}
	if methodType.NumOut() > 2 {
		return errors.New("返回值不能超过两个")
	}
	if methodType.NumOut() == 1 && methodType.Out(0).Implements(errorInterfaceType) {
		plan.actionErrorOnly = true
	}
	if methodType.NumOut() == 2 {
		if !methodType.Out(1).Implements(errorInterfaceType) {
			return errors.New("第二个返回值必须实现 error")
		}
		plan.actionReturnsError = true
	}
	return nil
}

func validateInitSignature(plan *controllerDispatchPlan) error {
	methodType := plan.initMethod.Type
	if methodType.IsVariadic() || methodType.NumIn() != 3 {
		return errors.New("必须接收 (*framework.App, *context.Request)")
	}
	if !appPointerType.AssignableTo(methodType.In(1)) || !requestPointerType.AssignableTo(methodType.In(2)) {
		return errors.New("参数必须能接收 *framework.App 和 *context.Request")
	}
	if methodType.NumOut() > 1 {
		return errors.New("最多返回一个 error")
	}
	if methodType.NumOut() == 1 {
		if !methodType.Out(0).Implements(errorInterfaceType) {
			return errors.New("返回值必须实现 error")
		}
		plan.initReturnsError = true
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
		handler := h.app.Middleware.ResolveAlias(declaration.Name)
		if handler == nil {
			return nil, fmt.Errorf("控制器中间件别名 %q 未注册", declaration.Name)
		}
		handlers = append(handlers, handler)
	}
	return handlers, nil
}

func validateControllerActionFilter(actions []string) error {
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
	if h.app != nil && h.app.Log != nil && err != nil {
		h.app.Log.ErrorCtx("HTTP 控制器分发失败", map[string]interface{}{
			"handler": handler,
			"error":   err.Error(),
		})
	}
	message := http.StatusText(http.StatusInternalServerError)
	if h.app != nil && h.app.IsDebug() && err != nil {
		message = err.Error()
	}
	return context.NewResponse().Code(http.StatusInternalServerError).Content(message)
}
