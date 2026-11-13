package framework

import (
	"errors"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/event"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
)

// TestRegisterGlobalComponents 验证全局注册保持中间件层级与请求工厂生命周期。
func TestRegisterGlobalComponents(t *testing.T) {
	app := NewConsoleAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })
	service := &componentTestService{}
	calls := 0
	err := app.RegisterGlobalComponents(GlobalApplicationComponents{
		Services: []interface{}{service},
		Middleware: []middleware.Handler{func(request *Request, next func(*Request) *Response) *Response {
			return next(request)
		}},
		Providers: map[string]interface{}{
			string(ServiceRequest): func() int { calls++; return calls },
			"shared":               "公共服务",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !service.registered || len(app.snapshotMiddlewareRegistry()) != 1 || len(app.snapshotApplicationMiddlewareRegistry()) != 0 {
		t.Fatal("全局服务或中间件层级不正确")
	}
	for want := 1; want <= 2; want++ {
		value, err := app.Make(string(ServiceRequest))
		if err != nil || value != want {
			t.Fatalf("请求工厂不应变为单例: %v %v", value, err)
		}
	}
}

// TestRegisterGlobalComponentsRejectsInvalidValues 验证非法组件保留明确错误。
func TestRegisterGlobalComponentsRejectsInvalidValues(t *testing.T) {
	var nilApp *App
	if err := nilApp.RegisterGlobalComponents(GlobalApplicationComponents{}); !errors.Is(err, ErrNilApplication) {
		t.Fatal(err)
	}
	for _, components := range []GlobalApplicationComponents{
		{Services: []interface{}{nil}},
		{Middleware: []middleware.Handler{nil}},
		{Providers: map[string]interface{}{serviceKeyApp: "invalid"}},
		{Events: EventDefinition{Subscribe: []event.Subscriber{nil}}},
	} {
		app := NewConsoleAppUninitialized(t.TempDir())
		t.Cleanup(func() { _ = app.Close() })
		if err := app.RegisterGlobalComponents(components); err == nil {
			t.Fatalf("非法全局组件必须报错: %#v", components)
		}
	}
}
