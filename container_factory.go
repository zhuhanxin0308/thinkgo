package framework

import (
	"fmt"
	"net/http"
	"reflect"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

// factoryPlan 仅属于当前绑定代际；重绑定直接替换，不维护跨绑定的全局可变缓存。
type factoryPlan struct {
	factory         reflect.Value
	fixedTypes      []reflect.Type
	variadicType    reflect.Type
	injectContainer bool
	outputError     error
	directCall      func(*Container, *resolution, []interface{}) (interface{}, error)
}

// compileFactoryPlan 在注册时分析不可变函数签名，未覆盖的签名继续使用完整反射调用。
func compileFactoryPlan(concrete interface{}) *factoryPlan {
	factory := reflect.ValueOf(concrete)
	if !factory.IsValid() || factory.Kind() != reflect.Func {
		return nil
	}
	factoryType := factory.Type()
	plan := &factoryPlan{factory: factory}
	firstInput := 0
	if factoryType.NumIn() > 0 && factoryType.In(0) == reflect.TypeFor[*Container]() {
		plan.injectContainer = true
		firstInput = 1
	}
	lastFixed := factoryType.NumIn()
	if factoryType.IsVariadic() {
		lastFixed--
		plan.variadicType = factoryType.In(lastFixed).Elem()
	}
	for index := firstInput; index < lastFixed; index++ {
		plan.fixedTypes = append(plan.fixedTypes, factoryType.In(index))
	}
	switch factoryType.NumOut() {
	case 0:
		plan.outputError = fmt.Errorf("工厂必须返回实例或 (实例, error)")
	case 1:
	case 2:
		if !factoryType.Out(1).Implements(reflect.TypeFor[error]()) {
			plan.outputError = fmt.Errorf("工厂第二个返回值必须实现 error")
		}
	default:
		plan.outputError = fmt.Errorf("工厂返回值数量必须为 1 或 2，实际为 %d", factoryType.NumOut())
	}
	plan.directCall = directFactoryCall(concrete)
	return plan
}

func (plan *factoryPlan) validateParameterCount(count int) error {
	required := len(plan.fixedTypes)
	if plan.variadicType != nil {
		if count < required {
			return fmt.Errorf("工厂参数不足：至少需要 %d 个，实际为 %d 个", required, count)
		}
	} else if count != required {
		return fmt.Errorf("工厂参数数量错误：需要 %d 个，实际为 %d 个", required, count)
	}
	return nil
}

// directFactoryCall 只依据函数类型选择调用方式，同签名的项目 Provider 自动获得相同路径。
// 服务名称、容器代际检查和作用域规则不参与此选择，也不会绕过正常解析流程。
func directFactoryCall(concrete interface{}) func(*Container, *resolution, []interface{}) (interface{}, error) {
	switch factory := concrete.(type) {
	case func() interface{}:
		return func(*Container, *resolution, []interface{}) (interface{}, error) { return factory(), nil }
	case func() (interface{}, error):
		return func(*Container, *resolution, []interface{}) (interface{}, error) { return factory() }
	case func(*Container) interface{}:
		return func(container *Container, scope *resolution, _ []interface{}) (interface{}, error) {
			return factory(container.scoped(scope)), nil
		}
	case func(*Container) (interface{}, error):
		return func(container *Container, scope *resolution, _ []interface{}) (interface{}, error) {
			return factory(container.scoped(scope))
		}
	case func(*http.Request, ...fwcontext.RequestOption) (*fwcontext.Request, error):
		return func(_ *Container, _ *resolution, params []interface{}) (interface{}, error) {
			return invokeVariadicFactory(factory, params)
		}
	case func(*Container, *http.Request, ...fwcontext.RequestOption) (*fwcontext.Request, error):
		return func(container *Container, scope *resolution, params []interface{}) (interface{}, error) {
			return invokeVariadicFactory(func(raw *http.Request, options ...fwcontext.RequestOption) (*fwcontext.Request, error) {
				return factory(container.scoped(scope), raw, options...)
			}, params)
		}
	default:
		return nil
	}
}

// invokeVariadicFactory 复用一项固定参数加可变参数的强类型调用，调用前数量已由签名计划验证。
func invokeVariadicFactory[First, Item, Result any](factory func(First, ...Item) (Result, error), params []interface{}) (interface{}, error) {
	first, err := typedFactoryArgument[First](params[0])
	if err != nil {
		return nil, fmt.Errorf("工厂第 1 个参数无效: %w", err)
	}
	// reflect.Call 的空可变参数切片也非 nil；直接调用必须保持可观察语义。
	items := make([]Item, len(params)-1)
	for index, parameter := range params[1:] {
		items[index], err = typedFactoryArgument[Item](parameter)
		if err != nil {
			return nil, fmt.Errorf("工厂第 %d 个参数无效: %w", index+2, err)
		}
	}
	return factory(first, items...)
}

// typedFactoryArgument 在常见精确类型上直接断言，保留 nil 和 Go 可赋值但动态类型不同的边界。
func typedFactoryArgument[T any](parameter interface{}) (T, error) {
	if value, ok := parameter.(T); ok {
		return value, nil
	}
	var zero T
	targetType := reflect.TypeFor[T]()
	value, err := reflectArgument(parameter, targetType)
	if err != nil {
		return zero, err
	}
	if parameter == nil {
		return zero, nil
	}
	// 已先按 AssignableTo 验证；转换只实现未命名函数、通道等合法赋值，不放宽参数类型。
	return value.Convert(targetType).Interface().(T), nil
}
