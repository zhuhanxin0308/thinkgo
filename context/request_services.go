package context

import (
	stdcontext "context"
	"errors"
	"io"
	"reflect"

	"github.com/zhuhanxin0308/thinkgo/v3/internal/resourceidentity"
)

var (
	// ErrRequestServiceScopeUnavailable 表示请求未绑定可用的服务作用域。
	ErrRequestServiceScopeUnavailable = errors.New("request service scope is unavailable")
)

// ServiceResolver 定义请求作用域解析服务所需的最小契约。
type ServiceResolver interface {
	Make(abstract string, params ...interface{}) (interface{}, error)
}

// ContextualServiceResolver 让请求把调用时的上下文传入支持该能力的服务作用域。
type ContextualServiceResolver interface {
	MakeContext(stdcontext.Context, string, ...interface{}) (interface{}, error)
}

type cleanupAwareServiceResolver interface {
	ServiceResolver
	HasRequestScopedResources() bool
}

// IdleServiceScopeCloser 是框架内部请求作用域可选的零分配关闭契约。
// 普通扩展即使实现同名方法，也只有通过显式受信选项绑定后才会在同步快速路径调用。
type IdleServiceScopeCloser interface {
	io.Closer
	CloseIfIdle() (bool, error)
}

// WithServiceScope 将服务解析器和生命周期关闭器绑定到请求。
func WithServiceScope(resolver ServiceResolver, closer io.Closer) RequestOption {
	return withServiceScope(resolver, closer, nil)
}

// WithTrustedIdleServiceScope 绑定框架自身管理的请求作用域，并允许在确认没有
// 在途构建和 Scoped 实例时同步零分配关闭。调用方不得把任意扩展实现作为受信探针。
func WithTrustedIdleServiceScope(resolver ServiceResolver, closer IdleServiceScopeCloser) RequestOption {
	return withServiceScope(resolver, closer, closer)
}

func withServiceScope(
	resolver ServiceResolver,
	closer io.Closer,
	idleCloser IdleServiceScopeCloser,
) RequestOption {
	return func(request *Request) error {
		if request == nil || isNilServiceScopeDependency(resolver) || isNilServiceScopeDependency(closer) {
			return ErrRequestServiceScopeUnavailable
		}
		if request.cleanupStarted.Load() {
			return ErrRequestCleaned
		}
		request.serviceMu.Lock()
		defer request.serviceMu.Unlock()
		if request.cleanupStarted.Load() || request.serviceClosed {
			return ErrRequestCleaned
		}
		if idleCloser == nil {
			request.cleanupNeedsSupervisor.Store(true)
		}
		request.serviceResolver = resolver
		if !isNilServiceScopeDependency(idleCloser) {
			request.serviceIdleCloser = idleCloser
		}
		for _, registered := range request.serviceClosers {
			if sameServiceScopeDependency(registered, closer) {
				return nil
			}
		}
		request.serviceClosers = append(request.serviceClosers, closer)
		return nil
	}
}

// Make 从当前请求作用域解析服务。
func (r *Request) Make(abstract string, params ...interface{}) (interface{}, error) {
	if r == nil {
		return nil, ErrRequestServiceScopeUnavailable
	}
	if r.cleanupStarted.Load() {
		return nil, ErrRequestServiceScopeUnavailable
	}
	resolver := r.beginServiceResolution()
	if resolver == nil {
		return nil, ErrRequestServiceScopeUnavailable
	}
	defer r.endServiceResolution()
	var resolved interface{}
	var err error
	if contextual, ok := resolver.(ContextualServiceResolver); ok {
		resolved, err = contextual.MakeContext(r.Context(), abstract, params...)
	} else {
		resolved, err = resolver.Make(abstract, params...)
	}
	if aware, ok := resolver.(cleanupAwareServiceResolver); !ok || aware.HasRequestScopedResources() {
		r.cleanupNeedsSupervisor.Store(true)
	}
	return resolved, err
}

// 解析器快照与准入计数在同一临界区取得。回调可以重绑定，原作用域仍由请求持有至统一清理。
func (r *Request) beginServiceResolution() ServiceResolver {
	r.serviceMu.Lock()
	defer r.serviceMu.Unlock()
	if r.cleanupStarted.Load() || r.serviceClosed || r.serviceResolver == nil {
		return nil
	}
	r.serviceResolving++
	return r.serviceResolver
}

func (r *Request) endServiceResolution() {
	r.serviceMu.Lock()
	defer r.serviceMu.Unlock()
	r.serviceResolving--
	if r.serviceResolving == 0 && r.serviceDrained != nil {
		close(r.serviceDrained)
	}
}

func sameServiceScopeDependency(left, right interface{}) bool {
	leftKey, leftIdentifiable := resourceidentity.Key(left)
	rightKey, rightIdentifiable := resourceidentity.Key(right)
	return leftIdentifiable && rightIdentifiable && leftKey == rightKey
}

func isNilServiceScopeDependency(value interface{}) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
