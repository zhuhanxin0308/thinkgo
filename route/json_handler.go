package route

import (
	"fmt"
	"net/http"
	"reflect"

	"github.com/zhuhanxin0308/thinkgo/framework/binding"
	"github.com/zhuhanxin0308/thinkgo/framework/context"
)

// JSONHandler 固定成功状态和 JSON 输出方式，完整匹配声明路径，不自动添加 URL 后缀。
// 函数参数继续由请求作用域注入。
// 实例只能通过构造函数创建，注册后不允许替换处理器或成功状态。
type JSONHandler struct {
	callback any
	status   int
	output   reflect.Type
}

// NewJSONHandler 校验函数签名；状态为零时，数据返回使用 200，空返回使用 204。
func NewJSONHandler(callback any, successStatus int) (*JSONHandler, error) {
	value := reflect.ValueOf(callback)
	if !value.IsValid() || value.Kind() != reflect.Func || value.IsNil() || value.Type().IsVariadic() {
		return nil, ErrInvalidRouteHandler
	}
	signature := value.Type()
	plan, err := binding.CompileCall(signature, false)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidRouteHandler, err)
	}
	output := plan.Output
	if output == reflect.TypeFor[*context.Response]() || output == reflect.TypeFor[context.Response]() || output != nil && output.Kind() == reflect.Interface {
		return nil, fmt.Errorf("%w: JSON 接口必须返回具体数据类型", ErrInvalidRouteHandler)
	}
	if signature.NumOut() == 2 && output == nil {
		return nil, fmt.Errorf("%w: 错误返回不能充当响应数据", ErrInvalidRouteHandler)
	}
	if successStatus == 0 {
		successStatus = http.StatusOK
		if output == nil {
			successStatus = http.StatusNoContent
		}
	}
	if successStatus < http.StatusOK || successStatus >= http.StatusMultipleChoices ||
		output != nil && (successStatus == http.StatusNoContent || successStatus == http.StatusResetContent) {
		return nil, fmt.Errorf("%w: 成功状态与返回类型不兼容", ErrInvalidRouteHandler)
	}
	return &JSONHandler{callback: callback, status: successStatus, output: output}, nil
}

// Callback 返回原始函数，供 HTTP 内核复用现有参数注入计划。
func (handler *JSONHandler) Callback() any { return handler.callback }

// SuccessStatus 返回声明的成功状态，错误响应由统一异常处理器决定。
func (handler *JSONHandler) SuccessStatus() int { return handler.status }

// OutputType 返回真实函数的数据类型；空返回及仅返回 error 时为 nil。
func (handler *JSONHandler) OutputType() reflect.Type { return handler.output }
