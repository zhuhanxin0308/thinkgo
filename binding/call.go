package binding

import (
	"fmt"
	"reflect"
)

// MaximumCallParameters 限制单个处理器的业务参数数量，不计方法接收者。
const MaximumCallParameters = 32

// ArgumentKind 区分请求值、显式输入对象和需要注入的依赖。
type ArgumentKind uint8

const (
	ArgumentValue ArgumentKind = iota
	ArgumentDependency
	ArgumentInput
)

// CallArgument 是启动期编译的参数定义；具体应用与作用域实例在调用时解析。
type CallArgument struct {
	Type     reflect.Type
	Kind     ArgumentKind
	Variadic bool
}

// CallPlan 统一控制器、普通路由和接口契约的签名规则，每次编译返回独立计划。
type CallPlan struct {
	Arguments    []CallArgument
	Input        reflect.Type
	Output       reflect.Type
	Variadic     bool
	ErrorOnly    bool
	ReturnsError bool
}

// CompileCall 校验实际函数签名，receiver 表示首个参数是反射方法的接收者。
func CompileCall(signature reflect.Type, receiver bool) (CallPlan, error) {
	var plan CallPlan
	if signature == nil || signature.Kind() != reflect.Func {
		return plan, fmt.Errorf("%w: 处理器必须是函数或方法", ErrDefinition)
	}
	start := 0
	if receiver {
		start = 1
	}
	if signature.NumIn() < start || signature.NumIn()-start > MaximumCallParameters {
		return plan, fmt.Errorf("%w: 处理器参数不能超过 %d 个", ErrDefinition, MaximumCallParameters)
	}
	plan.Input = reflect.TypeFor[Input]()
	plan.Variadic = signature.IsVariadic()
	inputCount := 0
	for index := start; index < signature.NumIn(); index++ {
		parameter := signature.In(index)
		argument := CallArgument{Type: parameter, Kind: ArgumentDependency}
		switch {
		case plan.Variadic && index == signature.NumIn()-1:
			if parameter.Kind() != reflect.Slice || !IsValueType(parameter.Elem()) || IsInputType(parameter.Elem()) {
				return plan, fmt.Errorf("%w: 可变参数元素类型 %s 不受支持", ErrDefinition, parameter)
			}
			argument.Kind, argument.Variadic = ArgumentValue, true
		case IsInputType(parameter):
			inputCount++
			if inputCount > 1 {
				return plan, fmt.Errorf("%w: 一个处理器只能声明一个请求类型，请合并字段来源", ErrDefinition)
			}
			if err := ValidateType(parameter); err != nil {
				return plan, err
			}
			argument.Kind, plan.Input = ArgumentInput, parameter
		case IsValueType(parameter):
			argument.Kind = ArgumentValue
		}
		plan.Arguments = append(plan.Arguments, argument)
	}
	if signature.NumOut() > 2 {
		return plan, fmt.Errorf("%w: 返回值不能超过两个", ErrDefinition)
	}
	errorType := reflect.TypeFor[error]()
	if signature.NumOut() == 1 && signature.Out(0).Implements(errorType) {
		plan.ErrorOnly = true
	}
	if signature.NumOut() == 2 {
		if !signature.Out(1).Implements(errorType) {
			return plan, fmt.Errorf("%w: 第二个返回值必须实现 error", ErrDefinition)
		}
		plan.ReturnsError = true
	}
	if signature.NumOut() > 0 && !signature.Out(0).Implements(errorType) {
		plan.Output = signature.Out(0)
	}
	return plan, nil
}

// IsValueType 只把没有方法的接口视为动态请求值；服务接口必须进入依赖注入。
func IsValueType(typ reflect.Type) bool {
	if typ == nil {
		return false
	}
	switch typ.Kind() {
	case reflect.Pointer, reflect.Slice:
		return IsValueType(typ.Elem())
	case reflect.Interface:
		return typ.NumMethod() == 0
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	default:
		return false
	}
}
