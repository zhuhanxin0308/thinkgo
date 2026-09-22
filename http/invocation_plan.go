package http

import (
	"reflect"

	"github.com/zhuhanxin0308/thinkgo/framework/binding"
	"github.com/zhuhanxin0308/thinkgo/framework/context"
)

// compileActionCall 将共享签名计划映射为 HTTP 注入动作，应用与请求身份只在这里分类。
func compileActionCall(signature reflect.Type, receiver bool) (binding.CallPlan, []controllerActionArgument, error) {
	plan, err := binding.CompileCall(signature, receiver)
	if err != nil {
		return plan, nil, err
	}
	arguments := make([]controllerActionArgument, 0, len(plan.Arguments))
	for _, item := range plan.Arguments {
		argument := controllerActionArgument{parameterType: item.Type, variadic: item.Variadic}
		switch {
		case item.Type == appPointerType:
			argument.kind = controllerActionArgumentApp
		case item.Type == requestPointerType:
			argument.kind = controllerActionArgumentRequest
		case item.Kind == binding.ArgumentInput:
			argument.kind = controllerActionArgumentInput
		case item.Kind == binding.ArgumentValue:
			argument.kind = controllerActionArgumentValue
		default:
			argument.kind = controllerActionArgumentService
		}
		arguments = append(arguments, argument)
	}
	return plan, arguments, nil
}

// invokeActionCall 统一反射调用与返回值处理，成功状态只影响成功出口。
func (h *Http) invokeActionCall(callback reflect.Value, arguments []reflect.Value, plan binding.CallPlan, request *context.Request, successStatus int, handlerName string) *context.Response {
	var results []reflect.Value
	if plan.Variadic {
		results = callback.CallSlice(arguments)
	} else {
		results = callback.Call(arguments)
	}
	if plan.ErrorOnly {
		if err := reflectResultError(results[0]); err != nil {
			return h.responseForRecoveredRequest(request, err)
		}
		return callbackEmptyResponse(successStatus)
	}
	if plan.ReturnsError {
		if err := reflectResultError(results[len(results)-1]); err != nil {
			return h.responseForRecoveredRequest(request, err)
		}
		results = results[:len(results)-1]
	}
	if len(results) == 0 {
		return callbackEmptyResponse(successStatus)
	}
	if successStatus != 0 {
		return context.NewResponse().Code(successStatus).Json(results[0].Interface())
	}
	response, err := h.toResponse(results[0])
	if err != nil {
		return h.dispatchInternalError(handlerName, err)
	}
	return response
}
