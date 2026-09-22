package http

import (
	"reflect"

	"github.com/zhuhanxin0308/thinkgo/framework"
	"github.com/zhuhanxin0308/thinkgo/framework/binding"
	"github.com/zhuhanxin0308/thinkgo/framework/context"
	"github.com/zhuhanxin0308/thinkgo/framework/validate"
)

// bindInputArgument 为每个请求创建独立输入快照，并复用应用时区执行日期规则。
func (h *Http) bindInputArgument(request *context.Request, parameter reflect.Type) (reflect.Value, error) {
	if h == nil || h.app == nil {
		return reflect.Value{}, framework.ErrNilApplication
	}
	typ := parameter
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	input := reflect.New(typ)
	if err := binding.Bind(request, input.Interface(), validate.WithLocation(h.app.Location())); err != nil {
		return reflect.Value{}, err
	}
	if parameter.Kind() == reflect.Pointer {
		return input, nil
	}
	return input.Elem(), nil
}
