package beego

import (
	"context"
	"net/http"
	"time"

	"github.com/beego/beego/v2/core/logs"
	framework "github.com/beego/beego/v2/server/web"
	beecontext "github.com/beego/beego/v2/server/web/context"
	"github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/adapters"
	"github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/business"
	"github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/serve"
)

type App struct {
	router  *framework.ControllerRegister
	address string
	timeout time.Duration
}

// New 使用Beego自己的FilterChain、ControllerRegister及BindJSON处理请求。
func New(service adapters.Service, address string) (*App, error) {
	logs.Reset()
	configuration := *framework.BConfig
	configuration.RunMode = framework.PROD
	configuration.CopyRequestBody = true
	configuration.EnableGzip = false
	configuration.EnableErrorsShow = false
	configuration.EnableErrorsRender = false
	configuration.MaxMemory = int64(service.Limits().MaximumBodyBytes)
	configuration.MaxUploadSize = configuration.MaxMemory
	configuration.Log.AccessLogs = false
	configuration.Log.EnableStaticLogs = false
	configuration.WebConfig.AutoRender = false
	configuration.WebConfig.EnableXSRF = false
	configuration.WebConfig.Session.SessionOn = false
	router := framework.NewControllerRegisterWithCfg(&configuration)
	app := &App{router: router, address: address, timeout: adapters.Timeout(service)}
	router.InsertFilterChain("/api/*", func(next framework.FilterFunc) framework.FilterFunc {
		return func(c *beecontext.Context) {
			ctx, cancel := context.WithTimeout(c.Request.Context(), app.timeout)
			defer cancel()
			c.Request = c.Request.WithContext(ctx)
			principal, err := service.Authenticate(c.Request.Header.Get("Authorization"))
			if err != nil {
				failure(c, err)
				return
			}
			c.Input.SetData(adapters.PrincipalKey, principal)
			if c.Request.Method == http.MethodPost {
				if err := adapters.PrepareBody(c.Request, configuration.MaxMemory); err != nil {
					failure(c, err)
					return
				}
			}
			next(c)
		}
	})
	router.Get("/healthz", func(c *beecontext.Context) { respond(c, http.StatusOK, map[string]string{"status": "ok"}) })
	router.Get("/api/catalog", func(c *beecontext.Context) {
		result, err := adapters.Catalog(service, c.Request.Context(), principal(c), c.Request.URL.RawQuery)
		if err != nil {
			failure(c, err)
			return
		}
		respond(c, http.StatusOK, result)
	})
	router.Get("/api/products/:id", func(c *beecontext.Context) {
		result, cache, err := service.Detail(c.Request.Context(), principal(c), c.Input.Param(":id"))
		if err != nil {
			failure(c, err)
			return
		}
		c.Output.Header("X-Benchmark-Cache", cache)
		respond(c, http.StatusOK, result)
	})
	router.Post("/api/orders", func(c *beecontext.Context) {
		var input business.OrderInput
		if err := c.BindJSON(&input); err != nil {
			failure(c, business.InvalidInput("JSON字段类型不正确"))
			return
		}
		result, replayed, err := service.CreateOrder(c.Request.Context(), principal(c), c.Request.Header.Get("Idempotency-Key"), input)
		if err != nil {
			failure(c, err)
			return
		}
		respond(c, adapters.OrderStatus(replayed), result)
	})
	router.Post("/api/tickets/:id/comments", func(c *beecontext.Context) {
		var input business.CommentInput
		if err := c.BindJSON(&input); err != nil {
			failure(c, business.InvalidInput("JSON字段类型不正确"))
			return
		}
		result, err := service.AddComment(c.Request.Context(), principal(c), c.Input.Param(":id"), input)
		if err != nil {
			failure(c, err)
			return
		}
		respond(c, http.StatusCreated, result)
	})
	router.Init()
	return app, nil
}

func principal(c *beecontext.Context) business.Principal {
	return c.Input.GetData(adapters.PrincipalKey).(business.Principal)
}
func respond(c *beecontext.Context, status int, body any) {
	c.Output.SetStatus(status)
	_ = c.Output.JSON(body, false, false)
}
func failure(c *beecontext.Context, err error) {
	status, body := business.ErrorResult(err)
	respond(c, status, body)
}
func (app *App) Handler() http.Handler { return app.router }
func (app *App) Run(ctx context.Context) error {
	return serve.HTTP(ctx, app.address, app.router, app.timeout)
}
func (app *App) Close() error { return nil }
