// Package resourceidentity 为资源所有者提供保守、无用户回调的实例身份判定。
package resourceidentity

import "reflect"

// Identity 仅标识具有稳定引用身份的资源；类型不同的包装保持各自关闭责任。
type Identity struct {
	typeOf  reflect.Type
	address uintptr
}

// Key 只对具有唯一分配地址的非空指针、通道和映射返回可靠身份。
// 函数代码地址、切片首地址、值相等以及可共享 runtime.zerobase 的零尺寸对象指针，
// 均不能证明两个关闭责任属于同一底层实例。
// 调用方在使用键期间必须继续持有资源，避免释放后的地址复用。
func Key(resource any) (Identity, bool) {
	value := reflect.ValueOf(resource)
	if !value.IsValid() {
		return Identity{}, false
	}
	switch value.Kind() {
	case reflect.Pointer:
		if !value.IsNil() && value.Type().Elem().Size() > 0 {
			return Identity{typeOf: value.Type(), address: value.Pointer()}, true
		}
	case reflect.Chan, reflect.Map:
		if !value.IsNil() {
			return Identity{typeOf: value.Type(), address: value.Pointer()}, true
		}
	}
	return Identity{}, false
}
