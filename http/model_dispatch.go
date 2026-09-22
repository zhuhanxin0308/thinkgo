package http

import (
	"reflect"

	"github.com/zhuhanxin0308/thinkgo/framework"
	"github.com/zhuhanxin0308/thinkgo/framework/context"
)

type actionModelResolver struct {
	http    *Http
	request *context.Request
}

func (resolver actionModelResolver) Make(name string, params ...interface{}) (interface{}, error) {
	return resolver.http.resolveController(resolver.request, name, params...)
}

func (h *Http) resolveActionModel(request *context.Request, target reflect.Type) (reflect.Value, error) {
	instance, err := framework.ResolveModelType(actionModelResolver{http: h, request: request}, target)
	if err != nil {
		return reflect.Value{}, err
	}
	return reflect.ValueOf(instance), nil
}
