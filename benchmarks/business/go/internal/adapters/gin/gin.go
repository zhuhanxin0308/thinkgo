package gin

import (
	"context"
	"io"
	"net/http"
	"time"

	framework "github.com/gin-gonic/gin"
	"github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/adapters"
	"github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/business"
	"github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/serve"
)

type App struct {
	router  *framework.Engine
	address string
	timeout time.Duration
}

// New 使用Gin原生路由、中间件、绑定器和JSON响应。
func New(service adapters.Service, address string) (*App, error) {
	framework.SetMode(framework.ReleaseMode)
	framework.DefaultWriter = io.Discard
	framework.DefaultErrorWriter = io.Discard
	router := framework.New()
	if err := router.SetTrustedProxies(nil); err != nil {
		return nil, err
	}
	app := &App{router: router, address: address, timeout: adapters.Timeout(service)}
	router.GET("/healthz", func(c *framework.Context) { c.JSON(http.StatusOK, framework.H{"status": "ok"}) })
	api := router.Group("/api")
	api.Use(func(c *framework.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), app.timeout)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		principal, err := service.Authenticate(c.GetHeader("Authorization"))
		if err != nil {
			failure(c, err)
			c.Abort()
			return
		}
		c.Set(adapters.PrincipalKey, principal)
		c.Next()
	})
	api.GET("/catalog", func(c *framework.Context) {
		result, err := adapters.Catalog(service, c.Request.Context(), principal(c), c.Request.URL.RawQuery)
		if err != nil {
			failure(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
	})
	api.GET("/products/:id", func(c *framework.Context) {
		result, cache, err := service.Detail(c.Request.Context(), principal(c), c.Param("id"))
		if err != nil {
			failure(c, err)
			return
		}
		c.Header("X-Benchmark-Cache", cache)
		c.JSON(http.StatusOK, result)
	})
	api.POST("/orders", func(c *framework.Context) {
		var input business.OrderInput
		if err := bind(c, service, &input); err != nil {
			failure(c, err)
			return
		}
		result, replayed, err := service.CreateOrder(c.Request.Context(), principal(c), c.GetHeader("Idempotency-Key"), input)
		if err != nil {
			failure(c, err)
			return
		}
		c.JSON(adapters.OrderStatus(replayed), result)
	})
	api.POST("/tickets/:id/comments", func(c *framework.Context) {
		var input business.CommentInput
		if err := bind(c, service, &input); err != nil {
			failure(c, err)
			return
		}
		result, err := service.AddComment(c.Request.Context(), principal(c), c.Param("id"), input)
		if err != nil {
			failure(c, err)
			return
		}
		c.JSON(http.StatusCreated, result)
	})
	return app, nil
}

func principal(c *framework.Context) business.Principal {
	return c.MustGet(adapters.PrincipalKey).(business.Principal)
}

func bind(c *framework.Context, service adapters.Service, target any) error {
	if err := adapters.PrepareBody(c.Request, int64(service.Limits().MaximumBodyBytes)); err != nil {
		return err
	}
	if err := c.ShouldBindJSON(target); err != nil {
		return business.InvalidInput("JSON字段类型不正确")
	}
	return nil
}

func failure(c *framework.Context, err error) {
	status, body := business.ErrorResult(err)
	c.JSON(status, body)
}

func (app *App) Handler() http.Handler { return app.router }
func (app *App) Run(ctx context.Context) error {
	return serve.HTTP(ctx, app.address, app.router, app.timeout)
}
func (app *App) Close() error { return nil }
