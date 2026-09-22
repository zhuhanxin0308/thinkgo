package binding

import (
	"fmt"
	"reflect"
)

// Input 是请求类型的零字段标记。匿名嵌入后，控制器或路由回调可直接接收结构体或单层指针。
// 普通服务和模型不会因为拥有 JSON 标签而被当成请求，每个处理器只声明一个聚合请求类型。
type Input struct{}

func (Input) requestInput() {}

var inputInterface = reflect.TypeOf((*interface{ requestInput() })(nil)).Elem()

// IsInputType 判断类型是否显式嵌入 Input，不实例化请求或执行用户方法。
func IsInputType(typ reflect.Type) bool {
	if typ == nil {
		return false
	}
	for depth := 0; typ.Kind() == reflect.Pointer && depth < maximumDepth; depth++ {
		typ = typ.Elem()
	}
	return typ.Kind() == reflect.Struct && typ.Implements(inputInterface)
}

// ValidateSignature 预编译函数或方法中的请求参数；服务参数和返回值仍由各调用层检查。
func ValidateSignature(signature reflect.Type) error {
	if signature == nil || signature.Kind() != reflect.Func {
		return fmt.Errorf("%w: 处理器必须是函数或方法", ErrDefinition)
	}
	inputs := 0
	for index := 0; index < signature.NumIn(); index++ {
		parameter := signature.In(index)
		if signature.IsVariadic() && index == signature.NumIn()-1 && IsInputType(parameter.Elem()) {
			return fmt.Errorf("%w: 请求参数不能是可变参数", ErrDefinition)
		}
		if !IsInputType(parameter) {
			continue
		}
		inputs++
		if inputs > 1 {
			return fmt.Errorf("%w: 一个处理器只能声明一个请求类型，请合并字段来源", ErrDefinition)
		}
		if err := ValidateType(parameter); err != nil {
			return err
		}
	}
	return nil
}
