package http

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/zhuhanxin0308/thinkgo/framework/binding"
	"github.com/zhuhanxin0308/thinkgo/framework/context"
	"github.com/zhuhanxin0308/thinkgo/framework/route"
)

type routeCallbackPlan struct {
	callbackType reflect.Type
	arguments    []controllerActionArgument
	variadic     bool
	errorOnly    bool
	returnsError bool
}

// invokeRouteCallback 对应 ThinkPHP route dispatch Callback::exec：容器负责
// 参数绑定，回调结果再经过统一的自动响应转换。
func (h *Http) invokeRouteCallback(callback reflect.Value, matchedRoute *route.Route, request *context.Request) *context.Response {
	return h.invokeRouteCallbackWithStatus(callback, matchedRoute, request, 0)
}

// invokeRouteCallbackWithStatus 复用同一参数计划，仅在成功出口应用已声明的 JSON 契约。
// 状态为零表示普通路由，继续保留原有自动响应转换行为。
func (h *Http) invokeRouteCallbackWithStatus(callback reflect.Value, matchedRoute *route.Route, request *context.Request, successStatus int) *context.Response {
	plan, err := h.resolveRouteCallbackPlan(callback.Type())
	if err != nil {
		return h.dispatchInternalError("route callback", fmt.Errorf("路由回调签名非法: %w", err))
	}
	arguments, err := h.bindRouteCallbackArguments(plan, matchedRoute, request)
	if err != nil {
		return h.responseForRecoveredRequest(request, err)
	}
	compiled := binding.CallPlan{Variadic: plan.variadic, ErrorOnly: plan.errorOnly, ReturnsError: plan.returnsError}
	return h.invokeActionCall(callback, arguments, compiled, request, successStatus, "route callback")
}

func callbackEmptyResponse(successStatus int) *context.Response {
	response := context.NewResponse()
	if successStatus != 0 {
		response.Code(successStatus)
	}
	return response
}

func (h *Http) resolveRouteCallbackPlan(callbackType reflect.Type) (*routeCallbackPlan, error) {
	if h == nil || callbackType == nil || callbackType.Kind() != reflect.Func {
		return nil, errors.New("路由回调类型非法")
	}
	h.routeCallbackPlanMu.RLock()
	cached := h.routeCallbackPlans[callbackType]
	h.routeCallbackPlanMu.RUnlock()
	if cached != nil {
		return cached, nil
	}

	plan := &routeCallbackPlan{callbackType: callbackType}
	if err := validateRouteCallbackPlan(plan); err != nil {
		return nil, err
	}
	h.routeCallbackPlanMu.Lock()
	if h.routeCallbackPlans == nil {
		h.routeCallbackPlans = make(map[reflect.Type]*routeCallbackPlan)
	}
	if existing := h.routeCallbackPlans[callbackType]; existing != nil {
		plan = existing
	} else {
		h.routeCallbackPlans[callbackType] = plan
	}
	h.routeCallbackPlanMu.Unlock()
	return plan, nil
}

func validateRouteCallbackPlan(plan *routeCallbackPlan) error {
	if plan == nil || plan.callbackType == nil {
		return errors.New("路由回调计划为空")
	}
	compiled, arguments, err := compileActionCall(plan.callbackType, false)
	if err != nil {
		return err
	}
	plan.arguments = arguments
	plan.variadic, plan.errorOnly, plan.returnsError = compiled.Variadic, compiled.ErrorOnly, compiled.ReturnsError
	return nil
}

func (h *Http) bindRouteCallbackArguments(plan *routeCallbackPlan, matchedRoute *route.Route, request *context.Request) ([]reflect.Value, error) {
	if plan == nil {
		return nil, errors.New("路由回调计划为空")
	}
	return h.bindActionArguments(nil, plan.arguments, matchedRoute, request)
}
