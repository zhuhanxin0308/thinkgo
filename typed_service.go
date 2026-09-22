package framework

import (
	"fmt"
	"reflect"
)

// TypedServiceKey 把稳定服务名与调用方期望的 Go 类型绑定在一起。
type TypedServiceKey[T any] struct {
	name string
}

// NewTypedServiceKey 创建经过名称校验的泛型服务键。
func NewTypedServiceKey[T any](name string) (TypedServiceKey[T], error) {
	if err := validateServiceName(ServiceName(name)); err != nil {
		return TypedServiceKey[T]{}, err
	}
	return TypedServiceKey[T]{name: name}, nil
}

// Name 返回可供旧版容器 API 使用的稳定名称。
func (key TypedServiceKey[T]) Name() string {
	return key.name
}

// TypedServiceResolver 定义泛型服务解析所需的最小容器契约。
type TypedServiceResolver interface {
	Make(abstract string, params ...interface{}) (interface{}, error)
}

// BindTyped 注册类型安全的单例工厂。
func BindTyped[T any](app *App, key TypedServiceKey[T], factory func(*Container) (T, error)) error {
	return bindTyped(app, key, factory, Singleton)
}

// BindFactoryTyped 注册类型安全的瞬时工厂。
func BindFactoryTyped[T any](app *App, key TypedServiceKey[T], factory func(*Container) (T, error)) error {
	return bindTyped(app, key, factory, Factory)
}

// BindScopedTyped 注册类型安全的请求或任务作用域工厂。
func BindScopedTyped[T any](app *App, key TypedServiceKey[T], factory func(*Container) (T, error)) error {
	return bindTyped(app, key, factory, Scoped)
}

// InstanceTyped 注册已经创建的类型安全单例。
func InstanceTyped[T any](app *App, key TypedServiceKey[T], instance T) error {
	if app == nil {
		return ErrNilApplication
	}
	if err := validateServiceName(ServiceName(key.name)); err != nil {
		return err
	}
	if isNilServiceInstance(instance) {
		return fmt.Errorf("%w: 服务 %q 的实例为空", ErrServiceUnavailable, key.name)
	}
	return app.Instance(key.name, instance)
}

// ResolveTyped 从应用、容器、请求或显式作用域解析并校验泛型服务类型。
func ResolveTyped[T any](resolver TypedServiceResolver, key TypedServiceKey[T], params ...interface{}) (T, error) {
	var zero T
	if err := validateServiceName(ServiceName(key.name)); err != nil {
		return zero, err
	}
	if isNilServiceInstance(resolver) {
		return zero, fmt.Errorf("%w: 服务 %q 缺少解析器", ErrServiceUnavailable, key.name)
	}
	instance, err := resolver.Make(key.name, params...)
	if err != nil {
		return zero, err
	}
	if isNilServiceInstance(instance) {
		return zero, fmt.Errorf("%w: 服务 %q 返回空实例", ErrServiceUnavailable, key.name)
	}
	service, ok := instance.(T)
	if !ok {
		expectedType := reflect.TypeOf((*T)(nil)).Elem()
		return zero, fmt.Errorf("%w: 服务 %q 期望类型 %s，实际类型 %T", ErrServiceTypeMismatch, key.name, expectedType, instance)
	}
	return service, nil
}

func bindTyped[T any](app *App, key TypedServiceKey[T], factory func(*Container) (T, error), lifecycle LifecycleType) error {
	if app == nil {
		return ErrNilApplication
	}
	if err := validateServiceName(ServiceName(key.name)); err != nil {
		return err
	}
	if factory == nil {
		return fmt.Errorf("%w: 服务 %q 的工厂为空", ErrServiceUnavailable, key.name)
	}
	wrapper := func(container *Container) (interface{}, error) {
		instance, err := factory(container)
		if err != nil {
			return nil, err
		}
		if isNilServiceInstance(instance) {
			return nil, fmt.Errorf("%w: 服务 %q 的工厂返回空实例", ErrServiceUnavailable, key.name)
		}
		return instance, nil
	}
	switch lifecycle {
	case Singleton:
		return app.Bind(key.name, wrapper)
	case Factory:
		return app.BindFactory(key.name, wrapper)
	case Scoped:
		return app.BindScoped(key.name, wrapper)
	default:
		return fmt.Errorf("%w: 服务 %q 的生命周期 %d 无效", ErrServiceUnavailable, key.name, lifecycle)
	}
}
