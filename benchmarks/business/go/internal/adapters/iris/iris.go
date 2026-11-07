package iris

import (
	"context"
	"io"
	"net/http"
	"time"

	framework "github.com/kataras/iris/v12"
	"github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/adapters"
	"github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/business"
	"github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/serve"
)

type App struct {
	router  *framework.Application
	address string
	timeout time.Duration
}

// New 保留Iris的路由上下文、原生中间件及ReadJSON/JSON执行路径。
func New(service adapters.Service, address string) (*App, error) {
	router := framework.New()
	router.Logger().SetOutput(io.Discard)
	app := &App{router: router, address: address, timeout: adapters.Timeout(service)}
	router.Get("/healthz", func(c framework.Context) { _ = c.JSON(framework.Map{"status": "ok"}) })
	api := router.Party("/api")
	api.Use(func(c framework.Context) {
		ctx, cancel := context.WithTimeout(c.Request().Context(), app.timeout)
		defer cancel()
		c.ResetRequest(c.Request().WithContext(ctx))
		principal, err := service.Authenticate(c.GetHeader("Authorization"))
		if err != nil {
			failure(c, err)
			return
		}
		c.Values().Set(adapters.PrincipalKey, principal)
		c.Next()
	})
	api.Get("/catalog", func(c framework.Context) {
		result, err := adapters.Catalog(service, c.Request().Context(), principal(c), c.Request().URL.RawQuery)
		if err != nil {
			failure(c, err)
			return
		}
		respond(c, http.StatusOK, result)
	})
	api.Get("/products/{id:string}", func(c framework.Context) {
		result, cache, err := service.Detail(c.Request().Context(), principal(c), c.Params().Get("id"))
		if err != nil {
			failure(c, err)
			return
		}
		c.Header("X-Benchmark-Cache", cache)
		respond(c, http.StatusOK, result)
	})
	api.Post("/orders", func(c framework.Context) {
		var input business.OrderInput
		if err := bind(c, service, &input); err != nil {
			failure(c, err)
			return
		}
		result, replayed, err := service.CreateOrder(c.Request().Context(), principal(c), c.GetHeader("Idempotency-Key"), input)
		if err != nil {
			failure(c, err)
			return
		}
		respond(c, adapters.OrderStatus(replayed), result)
	})
	api.Post("/tickets/{id:string}/comments", func(c framework.Context) {
		var input business.CommentInput
		if err := bind(c, service, &input); err != nil {
			failure(c, err)
			return
		}
		result, err := service.AddComment(c.Request().Context(), principal(c), c.Params().Get("id"), input)
		if err != nil {
			failure(c, err)
			return
		}
		respond(c, http.StatusCreated, result)
	})
	if err := router.Build(); err != nil {
		return nil, err
	}
	return app, nil
}

func principal(c framework.Context) business.Principal {
	return c.Values().Get(adapters.PrincipalKey).(business.Principal)
}
func bind(c framework.Context, service adapters.Service, target any) error {
	if err := adapters.PrepareBody(c.Request(), int64(service.Limits().MaximumBodyBytes)); err != nil {
		return err
	}
	if err := c.ReadJSON(target); err != nil {
		return business.InvalidInput("JSON字段类型不正确")
	}
	return nil
}
func respond(c framework.Context, status int, body any) { c.StatusCode(status); _ = c.JSON(body) }
func failure(c framework.Context, err error) {
	status, body := business.ErrorResult(err)
	respond(c, status, body)
}
func (app *App) Handler() http.Handler { return app.router }
func (app *App) Run(ctx context.Context) error {
	return serve.HTTP(ctx, app.address, app.router, app.timeout)
}
func (app *App) Close() error { return nil }
