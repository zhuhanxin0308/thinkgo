package thinkgo

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/adapters"
	"github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/business"
	framework "github.com/zhuhanxin0308/thinkgo/v3"
	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	fwhttp "github.com/zhuhanxin0308/thinkgo/v3/http"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
)

type App struct {
	application *framework.App
	kernel      *fwhttp.Http
	directory   string
}

// New 按原生多应用契约注册路由，正式运行由App.Run托管完整生命周期。
func New(service adapters.Service, address string) (*App, error) {
	directory, err := createProject(service, address)
	if err != nil {
		return nil, err
	}
	application := framework.NewAppUninitialized(directory)
	app := &App{application: application, directory: directory}
	err = application.RegisterApplications(func(current *framework.App) error {
		return current.RegisterGlobalMiddleware(guard(service))
	}, framework.ApplicationDefinition{
		Name: "index",
		Register: func(current *framework.App) error {
			return current.RegisterRouteLoader(func(loaded *framework.App) error {
				registerRoutes(loaded, service)
				return nil
			})
		},
	})
	if err != nil {
		return nil, errors.Join(err, app.Close())
	}
	app.kernel, err = fwhttp.NewHttp(application)
	if err != nil {
		return nil, errors.Join(err, app.Close())
	}
	return app, nil
}

// guard 在原生全局中间件阶段校验正文，使语法和绑定失败保持统一422协议。
func guard(service adapters.Service) middleware.Handler {
	return func(request *fwcontext.Request, next middleware.Next) *fwcontext.Response {
		if !strings.HasPrefix(request.Path(), "/api/") {
			return next(request)
		}
		ctx, cancel := context.WithTimeout(request.Context(), adapters.Timeout(service))
		defer cancel()
		*request.Raw() = *request.Raw().WithContext(ctx)
		principal, err := service.Authenticate(request.Header("Authorization"))
		if err != nil {
			return failure(err)
		}
		request.Set(adapters.PrincipalKey, principal)
		if request.Method() == http.MethodPost {
			if err := adapters.ValidateMedia(request.Header("Content-Type"), request.Header("Content-Encoding")); err != nil {
				return failure(err)
			}
			data, err := request.Body()
			if err != nil {
				return failure(business.InvalidInput("无法读取JSON正文"))
			}
			if err := adapters.ValidateObject(data, service.Limits().MaximumBodyBytes); err != nil {
				return failure(err)
			}
		}
		return next(request)
	}
}

func registerRoutes(application *framework.App, service adapters.Service) {
	router := application.Route()
	router.Get("/healthz", func(*fwcontext.Request) *fwcontext.Response {
		return respond(http.StatusOK, map[string]string{"status": "ok"})
	})
	router.Get("/api/catalog", func(request *fwcontext.Request) *fwcontext.Response {
		result, err := adapters.Catalog(service, request.Context(), principal(request), request.Raw().URL.RawQuery)
		if err != nil {
			return failure(err)
		}
		return respond(http.StatusOK, result)
	})
	router.Get("/api/products/:id", func(request *fwcontext.Request) *fwcontext.Response {
		result, cache, err := service.Detail(request.Context(), principal(request), request.Route("id"))
		if err != nil {
			return failure(err)
		}
		return respond(http.StatusOK, result).Header("X-Benchmark-Cache", cache)
	})
	router.Post("/api/orders", func(request *fwcontext.Request) *fwcontext.Response {
		var input business.OrderInput
		if err := bind(request, &input); err != nil {
			return failure(err)
		}
		result, replayed, err := service.CreateOrder(request.Context(), principal(request), request.Header("Idempotency-Key"), input)
		if err != nil {
			return failure(err)
		}
		return respond(adapters.OrderStatus(replayed), result)
	})
	router.Post("/api/tickets/:id/comments", func(request *fwcontext.Request) *fwcontext.Response {
		var input business.CommentInput
		if err := bind(request, &input); err != nil {
			return failure(err)
		}
		result, err := service.AddComment(request.Context(), principal(request), request.Route("id"), input)
		if err != nil {
			return failure(err)
		}
		return respond(http.StatusCreated, result)
	})
}

func principal(request *fwcontext.Request) business.Principal {
	return request.GetData(adapters.PrincipalKey).(business.Principal)
}
func bind(request *fwcontext.Request, target any) error {
	if err := request.Json(target); err != nil {
		return business.InvalidInput("JSON字段类型不正确")
	}
	return nil
}
func respond(status int, body any) *fwcontext.Response {
	return fwcontext.NewResponse().Code(status).Json(body)
}
func failure(err error) *fwcontext.Response {
	status, body := business.ErrorResult(err)
	return respond(status, body)
}
func (app *App) Handler() http.Handler { return app.kernel }

// contextKernel 保持App.Run的Provider、租约和资源关闭语义，并传入宿主取消信号。
type contextKernel struct {
	server *fwhttp.Server
	ctx    context.Context
}

func (kernel contextKernel) Run() error { return kernel.server.RunContext(kernel.ctx) }
func (app *App) Run(ctx context.Context) error {
	app.application.Kernel = contextKernel{fwhttp.NewServer(app.kernel), ctx}
	return app.application.Run()
}
func (app *App) Close() error {
	return errors.Join(app.application.Close(), os.RemoveAll(app.directory))
}
