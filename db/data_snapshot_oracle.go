//go:build oracle

package db

import (
	"reflect"

	"github.com/godror/godror"
)

// snapshotNativeDatabaseValue 复制 LOB 值外壳，按已约定的执行期流式语义借用 Reader。
// 不提前读取、关闭或假装复制任意流；调用方负责流在执行期间的生命周期与并发互斥。
func snapshotNativeDatabaseValue(value reflect.Value) (reflect.Value, bool) {
	if value.Type() == reflect.TypeFor[godror.Lob]() {
		return value, true
	}
	return reflect.Value{}, false
}
