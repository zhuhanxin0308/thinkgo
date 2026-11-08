package fiber

import (
	"context"
	"errors"
	"net/http"
	"time"

	framework "github.com/gofiber/fiber/v3"
	"github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/adapters"
	"github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/business"
)

type App struct {
	Router  *framework.App
	address string
	timeout time.Duration
}

// New 保持Fiber/fasthttp原生请求路径，不转换为net/http处理器。
func New(service adapters.Service, address string) (*App, error) {
	timeout := adapters.Timeout(service)
	router := framework.New(framework.Config{
		BodyLimit:   int(service.Limits().MaximumBodyBytes),
		ReadTimeout: timeout, WriteTimeout: timeout, IdleTimeout: timeout,
	})
	app := &App{Router: router, address: address, timeout: timeout}
	router.Get("/healthz", func(c framework.Ctx) error { return c.JSON(framework.Map{"status": "ok"}) })
	api := router.Group("/api", func(c framework.Ctx) error {
		ctx, cancel := context.WithTimeout(c.Context(), timeout)
		defer cancel()
		c.SetContext(ctx)
		principal, err := service.Authenticate(c.Get("Authorization"))
		if err != nil {
			return failure(c, err)
		}
		c.Locals(adapters.PrincipalKey, principal)
		return c.Next()
	})
	api.Get("/catalog", func(c framework.Ctx) error {
		result, err := adapters.Catalog(service, c.Context(), principal(c), string(c.Request().URI().QueryString()))
		if err != nil {
			return failure(c, err)
		}
		return c.Status(http.StatusOK).JSON(result)
	})
	api.Get("/products/:id", func(c framework.Ctx) error {
		result, cache, err := service.Detail(c.Context(), principal(c), c.Params("id"))
		if err != nil {
			return failure(c, err)
		}
		c.Set("X-Benchmark-Cache", cache)
		return c.Status(http.StatusOK).JSON(result)
	})
	api.Post("/orders", func(c framework.Ctx) error {
		var input business.OrderInput
		if err := bind(c, service, &input); err != nil {
			return failure(c, err)
		}
		result, replayed, err := service.CreateOrder(c.Context(), principal(c), c.Get("Idempotency-Key"), input)
		if err != nil {
			return failure(c, err)
		}
		return c.Status(adapters.OrderStatus(replayed)).JSON(result)
	})
	api.Post("/tickets/:id/comments", func(c framework.Ctx) error {
		var input business.CommentInput
		if err := bind(c, service, &input); err != nil {
			return failure(c, err)
		}
		result, err := service.AddComment(c.Context(), principal(c), c.Params("id"), input)
		if err != nil {
			return failure(c, err)
		}
		return c.Status(http.StatusCreated).JSON(result)
	})
	return app, nil
}

func principal(c framework.Ctx) business.Principal {
	return c.Locals(adapters.PrincipalKey).(business.Principal)
}
func bind(c framework.Ctx, service adapters.Service, target any) error {
	if err := adapters.ValidateMedia(c.Get("Content-Type"), c.Get("Content-Encoding")); err != nil {
		return err
	}
	if err := adapters.ValidateObject(c.Body(), int64(service.Limits().MaximumBodyBytes)); err != nil {
		return err
	}
	if err := c.Bind().JSON(target); err != nil {
		return business.InvalidInput("JSON字段类型不正确")
	}
	return nil
}
func failure(c framework.Ctx, err error) error {
	status, body := business.ErrorResult(err)
	return c.Status(status).JSON(body)
}
func (app *App) Run(ctx context.Context) error {
	return app.Router.Listen(app.address, framework.ListenConfig{
		GracefulContext: ctx, ShutdownTimeout: app.timeout, DisableStartupMessage: true,
	})
}
func (app *App) Close() error {
	err := app.Router.ShutdownWithTimeout(app.timeout)
	if errors.Is(err, framework.ErrNotRunning) {
		return nil
	}
	return err
}
