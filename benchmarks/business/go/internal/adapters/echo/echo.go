package echo

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"time"

	framework "github.com/labstack/echo/v5"
	"github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/adapters"
	"github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/business"
)

type App struct {
	router  *framework.Echo
	address string
	timeout time.Duration
}

// New 将所有业务路由注册在Echo原生认证中间件之后。
func New(service adapters.Service, address string) (*App, error) {
	router := framework.New()
	router.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	app := &App{router: router, address: address, timeout: adapters.Timeout(service)}
	router.GET("/healthz", func(c *framework.Context) error { return c.JSON(http.StatusOK, map[string]string{"status": "ok"}) })
	api := router.Group("/api")
	api.Use(func(next framework.HandlerFunc) framework.HandlerFunc {
		return func(c *framework.Context) error {
			ctx, cancel := context.WithTimeout(c.Request().Context(), app.timeout)
			defer cancel()
			c.SetRequest(c.Request().WithContext(ctx))
			principal, err := service.Authenticate(c.Request().Header.Get("Authorization"))
			if err != nil {
				return failure(c, err)
			}
			c.Set(adapters.PrincipalKey, principal)
			return next(c)
		}
	})
	api.GET("/catalog", func(c *framework.Context) error {
		result, err := adapters.Catalog(service, c.Request().Context(), principal(c), c.Request().URL.RawQuery)
		if err != nil {
			return failure(c, err)
		}
		return c.JSON(http.StatusOK, result)
	})
	api.GET("/products/:id", func(c *framework.Context) error {
		result, cache, err := service.Detail(c.Request().Context(), principal(c), c.Param("id"))
		if err != nil {
			return failure(c, err)
		}
		c.Response().Header().Set("X-Benchmark-Cache", cache)
		return c.JSON(http.StatusOK, result)
	})
	api.POST("/orders", func(c *framework.Context) error {
		var input business.OrderInput
		if err := bind(c, service, &input); err != nil {
			return failure(c, err)
		}
		result, replayed, err := service.CreateOrder(c.Request().Context(), principal(c), c.Request().Header.Get("Idempotency-Key"), input)
		if err != nil {
			return failure(c, err)
		}
		return c.JSON(adapters.OrderStatus(replayed), result)
	})
	api.POST("/tickets/:id/comments", func(c *framework.Context) error {
		var input business.CommentInput
		if err := bind(c, service, &input); err != nil {
			return failure(c, err)
		}
		result, err := service.AddComment(c.Request().Context(), principal(c), c.Param("id"), input)
		if err != nil {
			return failure(c, err)
		}
		return c.JSON(http.StatusCreated, result)
	})
	return app, nil
}

func principal(c *framework.Context) business.Principal {
	return c.Get(adapters.PrincipalKey).(business.Principal)
}
func bind(c *framework.Context, service adapters.Service, target any) error {
	if err := adapters.PrepareBody(c.Request(), int64(service.Limits().MaximumBodyBytes)); err != nil {
		return err
	}
	if err := c.Bind(target); err != nil {
		return business.InvalidInput("JSON字段类型不正确")
	}
	return nil
}
func failure(c *framework.Context, err error) error {
	status, body := business.ErrorResult(err)
	return c.JSON(status, body)
}
func (app *App) Handler() http.Handler { return app.router }
func (app *App) Run(ctx context.Context) error {
	configuration := framework.StartConfig{
		Address: app.address, HideBanner: true, HidePort: true, GracefulTimeout: app.timeout,
		BeforeServeFunc: func(server *http.Server) error {
			server.ReadHeaderTimeout = app.timeout
			server.ReadTimeout = app.timeout
			server.WriteTimeout = app.timeout
			server.IdleTimeout = app.timeout
			return nil
		},
	}
	return configuration.Start(ctx, app.router)
}
func (app *App) Close() error { return nil }
