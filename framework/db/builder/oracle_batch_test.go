//go:build oracle
// +build oracle

package builder

import (
	"reflect"
	"testing"
)

// TestOracleInsertBatchUsesInsertAll 验证 Oracle 12c+ 批量写入使用合法的 INSERT ALL，
// 并保持字段排序对应的逐行参数顺序。
func TestOracleInsertBatchUsesInsertAll(t *testing.T) {
	oracle := &Oracle{}
	if got := oracle.MaxBindParams(); got != 999 {
		t.Fatalf("Oracle INSERT ALL 目标列预算错误: got=%d want=999", got)
	}
	query, values := oracle.InsertBatch(
		"users",
		[]string{"email", "name"},
		[]map[string]interface{}{
			{"email": "alice@example.com", "name": "alice"},
			{"email": "bob@example.com", "name": "bob"},
		},
	)
	wantSQL := "INSERT ALL INTO \"users\" (\"email\", \"name\") VALUES (:1, :2) " +
		"INTO \"users\" (\"email\", \"name\") VALUES (:3, :4) SELECT 1 FROM DUAL"
	if got := oracle.Rebind(query); got != wantSQL {
		t.Fatalf("Oracle 批量插入 SQL 错误:\n got %q\nwant %q", got, wantSQL)
	}
	wantValues := []interface{}{"alice@example.com", "alice", "bob@example.com", "bob"}
	if !reflect.DeepEqual(values, wantValues) {
		t.Fatalf("Oracle 批量插入参数顺序错误: got=%#v want=%#v", values, wantValues)
	}
}
