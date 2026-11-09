// Package resourceidentity 为资源所有者提供保守、无用户回调的实例身份判定。
package resourceidentity

import "reflect"

// Identity 仅标识具有稳定引用身份的资源；类型不同的包装保持各自关闭责任。
type Identity struct {
	typeOf  reflect.Type
	address uintptr
}

// Key 只对非空指针、通道和映射返回可靠身份。
// 函数代码地址、切片首地址以及值相等均不能证明两个关闭责任属于同一实例。
// 调用方在使用键期间必须继续持有资源，避免释放后的地址复用。
func Key(resource any) (Identity, bool) {
	value := reflect.ValueOf(resource)
	if !value.IsValid() {
		return Identity{}, false
	}
	switch value.Kind() {
	case reflect.Pointer, reflect.Chan, reflect.Map:
		if !value.IsNil() {
			return Identity{typeOf: value.Type(), address: value.Pointer()}, true
		}
	}
	return Identity{}, false
}
