package route

import (
	"fmt"
	"reflect"

	"github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
)

// WithoutMiddleware 仅在目标函数指针唯一时移除，歧义时失败关闭。
func (r *Route) WithoutMiddleware(targets ...middleware.Handler) error {
	router, err := r.mutableRouter()
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return nil
	}
	if err = validateMiddleware(targets); err != nil {
		return err
	}
	router.mu.Lock()
	defer router.mu.Unlock()
	if router.frozen {
		return ErrRouterFrozen
	}
	working := append([]middleware.Handler(nil), r.middlewares...)
	for _, target := range targets {
		pointer := reflect.ValueOf(target).Pointer()
		matchedIndex := -1
		for index, candidate := range working {
			if reflect.ValueOf(candidate).Pointer() != pointer {
				continue
			}
			if matchedIndex >= 0 {
				return fmt.Errorf("%w: 中间件函数身份存在歧义", ErrInvalidRouteMiddleware)
			}
			matchedIndex = index
		}
		if matchedIndex < 0 {
			return fmt.Errorf("%w: 待移除中间件不存在", ErrInvalidRouteMiddleware)
		}
		working = append(working[:matchedIndex], working[matchedIndex+1:]...)
	}
	r.middlewares = working
	r.middlewarePipeline = buildMiddlewarePipeline(working)
	return nil
}

// WithMiddleware 在路由冻结前追加中间件。
func (r *Route) WithMiddleware(handlers ...middleware.Handler) error {
	router, err := r.mutableRouter()
	if err != nil {
		return err
	}
	if len(handlers) == 0 {
		return nil
	}
	if err = validateMiddleware(handlers); err != nil {
		return err
	}
	router.mu.Lock()
	defer router.mu.Unlock()
	if router.frozen {
		return ErrRouterFrozen
	}
	r.middlewares = append(r.middlewares, handlers...)
	r.middlewarePipeline = buildMiddlewarePipeline(r.middlewares)
	return nil
}

// Middlewares 返回防御性副本。
func (r *Route) Middlewares() []middleware.Handler {
	if r == nil {
		return nil
	}
	if r.router != nil {
		r.router.mu.RLock()
		defer r.router.mu.RUnlock()
	}
	return append([]middleware.Handler(nil), r.middlewares...)
}

// HasMiddleware 判断路由是否配置中间件，避免热路径为确认空管道复制中间件切片。
func (r *Route) HasMiddleware() bool {
	if r == nil {
		return false
	}
	if r.router != nil {
		r.router.mu.RLock()
		hasMiddleware := r.middlewarePipeline != nil
		r.router.mu.RUnlock()
		return hasMiddleware
	}
	return len(r.middlewares) > 0
}

// ExecuteMiddleware 使用路由注册阶段构建的不可变管道执行中间件，避免每个请求重复复制和装配管道。
func (r *Route) ExecuteMiddleware(request *context.Request, destination func(*context.Request) *context.Response) *context.Response {
	if r == nil {
		return destination(request)
	}
	if r.router != nil {
		r.router.mu.RLock()
		pipeline := r.middlewarePipeline
		r.router.mu.RUnlock()
		if pipeline != nil {
			return pipeline.Then(request, destination)
		}
	}
	return destination(request)
}

// buildMiddlewarePipeline 在路由注册或修改时一次性装配管道，运行期间只读共享。
func buildMiddlewarePipeline(handlers []middleware.Handler) *middleware.Pipeline {
	if len(handlers) == 0 {
		return nil
	}
	pipeline := middleware.NewPipeline()
	for _, handler := range handlers {
		pipeline.Pipe(handler)
	}
	return pipeline
}

func validateMiddleware(handlers []middleware.Handler) error {
	for index, handler := range handlers {
		if handler == nil {
			return fmt.Errorf("%w: 第 %d 项为空", ErrInvalidRouteMiddleware, index+1)
		}
	}
	return nil
}

func mergeMiddleware(parent, current []middleware.Handler) []middleware.Handler {
	merged := make([]middleware.Handler, 0, len(parent)+len(current))
	merged = append(merged, parent...)
	merged = append(merged, current...)
	return merged
}
