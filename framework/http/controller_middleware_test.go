package http

import (
	stdhttp "net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"thinkgo/framework"
	"thinkgo/framework/config"
	fwcontext "thinkgo/framework/context"
	"thinkgo/framework/log"
	"thinkgo/framework/middleware"
	"thinkgo/framework/route"
)

// mwTestController 用于验证控制器级中间件声明会被分发器实际应用。
type mwTestController struct {
	framework.Controller
}

// Init 在初始化阶段声明控制器级中间件，且仅对 Guarded 动作生效。
func (c *mwTestController) Init(app *framework.App, req *fwcontext.Request) {
	c.Controller.Init(app, req)
	c.SetMiddleware(framework.ControllerMiddleware{Name: "tap", Only: []string{"Guarded"}})
}

// Guarded 受控制器中间件保护的动作。
func (c *mwTestController) Guarded(req *fwcontext.Request) *fwcontext.Response {
	return fwcontext.NewResponse().Content("guarded-action")
}

// Open 未被 Only 命中的动作，不应触发控制器中间件。
func (c *mwTestController) Open(req *fwcontext.Request) *fwcontext.Response {
	return fwcontext.NewResponse().Content("open-action")
}

// newControllerTestApp 构建带容器的最小应用，供控制器分发相关测试使用。
func newControllerTestApp(t *testing.T) *framework.App {
	t.Helper()

	cfg := config.NewConfig()
	cfg.Set("app.server", map[string]interface{}{"host": "127.0.0.1", "port": 8080})
	cfg.Set("app.compression", map[string]interface{}{"enable": false})

	app := &framework.App{
		Container:  framework.NewContainer(),
		BasePath:   t.TempDir(),
		Config:     cfg,
		Route:      route.NewRouter(),
		Middleware: middleware.NewPipeline(),
		Log:        log.NewLog(),
	}
	t.Cleanup(app.Log.Shutdown)
	return app
}

// TestControllerMiddlewareApplied 验证 Only 命中的动作会执行控制器声明的中间件。
func TestControllerMiddlewareApplied(t *testing.T) {
	app := newControllerTestApp(t)
	app.BindFactory("MwCtrl", reflect.TypeOf(mwTestController{}))

	// 注册中间件别名：命中时为响应打上标记头。
	app.Middleware.Alias("tap", func(req *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
		resp := next(req)
		if resp != nil {
			resp.Header("X-Controller-Mw", "1")
		}
		return resp
	})

	app.Route.Get("/guarded", "MwCtrl@Guarded")
	app.Route.Get("/open", "MwCtrl@Open")

	handler := NewHttp(app)

	// Only 命中：中间件应执行。
	guardedRec := httptest.NewRecorder()
	handler.ServeHTTP(guardedRec, httptest.NewRequest(stdhttp.MethodGet, "http://example.com/guarded", nil))
	if guardedRec.Body.String() != "guarded-action" {
		t.Fatalf("受控动作响应内容不正确: %q", guardedRec.Body.String())
	}
	if guardedRec.Header().Get("X-Controller-Mw") != "1" {
		t.Fatal("控制器级中间件未对 Only 命中的动作生效")
	}

	// 未命中 Only：中间件不应执行。
	openRec := httptest.NewRecorder()
	handler.ServeHTTP(openRec, httptest.NewRequest(stdhttp.MethodGet, "http://example.com/open", nil))
	if openRec.Body.String() != "open-action" {
		t.Fatalf("普通动作响应内容不正确: %q", openRec.Body.String())
	}
	if openRec.Header().Get("X-Controller-Mw") != "" {
		t.Fatal("控制器级中间件不应对未命中 Only 的动作生效")
	}
}
