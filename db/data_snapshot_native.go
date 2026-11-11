//go:build !oracle

package db

import "reflect"

// snapshotNativeDatabaseValue 在未启用 Oracle 时不引入 OCI 驱动或资源句柄例外。
func snapshotNativeDatabaseValue(reflect.Value) (reflect.Value, bool) {
	return reflect.Value{}, false
}
