//go:build oracle
// +build oracle

package builder

import (
	"errors"
	"reflect"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework/db"
)

func TestOracleTypedLockCapabilities(t *testing.T) {
	oracle := &Oracle{}
	update, err := oracle.Lock(db.LockForUpdate)
	if err != nil || update.Tail != " FOR UPDATE" {
		t.Fatalf("Oracle update lock mismatch: spec=%#v err=%v", update, err)
	}
	if _, err := oracle.Lock(db.LockForShare); !errors.Is(err, db.ErrUnsupportedFeature) {
		t.Fatalf("Oracle share lock must be explicitly unsupported: %v", err)
	}
}

func TestOracleLogicalIdentifiersAndLockedPagination(t *testing.T) {
	oracle := &Oracle{}
	if got := oracle.QuoteIdentifier("app.users"); got != `"APP"."USERS"` {
		t.Fatalf("Oracle logical identifier mismatch: %s", got)
	}
	if got := oracle.QuoteFields("app.users.id AS user_id"); got != `"APP"."USERS"."ID" AS "USER_ID"` {
		t.Fatalf("Oracle logical field mismatch: %s", got)
	}
	if order, _ := oracle.Pagination("created_at DESC", 0, 0); order != ` ORDER BY "CREATED_AT" DESC` {
		t.Fatalf("Oracle logical order mismatch: %s", order)
	}
	database := db.NewDB(&db.SQLConnection{Builder: oracle})
	query := database.Name("users").WhereField("status", "=", 1).Order("id").Limit(10).Lock()
	if _, _, err := query.BuildSelectSQL(); !errors.Is(err, db.ErrUnsupportedFeature) {
		t.Fatalf("Oracle locked pagination must be explicitly unsupported: %v", err)
	}
}

// TestOracleInsertBatchUsesDistinctSourceRows 验证 Oracle 批量写入为每条记录创建独立源行，
// 并保持字段排序对应的逐行参数顺序。
func TestOracleInsertBatchUsesDistinctSourceRows(t *testing.T) {
	oracle := &Oracle{}
	if oracle.MaxBatchRows() != 127 {
		t.Fatal("Oracle 批量分支数量没有遵守数据库上限")
	}
	if got := oracle.MaxBindParams(); got != 999 {
		t.Fatalf("Oracle 批量绑定预算错误: got=%d want=999", got)
	}
	query, values := oracle.InsertBatch(
		"users",
		[]string{"email", "name"},
		[]map[string]interface{}{
			{"email": "alice@example.com", "name": "alice"},
			{"email": "bob@example.com", "name": "bob"},
		},
	)
	wantSQL := "INSERT ALL WHEN thinkgo_batch_row = 1 THEN INTO \"USERS\" (\"EMAIL\", \"NAME\") VALUES (:1, :2) " +
		"WHEN thinkgo_batch_row = 2 THEN INTO \"USERS\" (\"EMAIL\", \"NAME\") VALUES (:3, :4) " +
		"SELECT LEVEL AS thinkgo_batch_row FROM DUAL CONNECT BY LEVEL <= 2"
	if got := oracle.Rebind(query); got != wantSQL {
		t.Fatalf("Oracle 批量插入 SQL 错误:\n got %q\nwant %q", got, wantSQL)
	}
	wantValues := []interface{}{"alice@example.com", "alice", "bob@example.com", "bob"}
	if !reflect.DeepEqual(values, wantValues) {
		t.Fatalf("Oracle 批量插入参数顺序错误: got=%#v want=%#v", values, wantValues)
	}
}

// TestOracleInsertBatchNullBinding 保留原始绑定值，类型转换由每行目标列决定。
func TestOracleInsertBatchNullBinding(t *testing.T) {
	oracle := &Oracle{}
	query, values := oracle.InsertBatch("items", []string{"value"}, []map[string]interface{}{
		{"value": nil}, {"value": "literal NULL"},
	})
	want := "INSERT ALL WHEN thinkgo_batch_row = 1 THEN INTO \"ITEMS\" (\"VALUE\") VALUES (:1) " +
		"WHEN thinkgo_batch_row = 2 THEN INTO \"ITEMS\" (\"VALUE\") VALUES (:2) " +
		"SELECT LEVEL AS thinkgo_batch_row FROM DUAL CONNECT BY LEVEL <= 2"
	if oracle.Rebind(query) != want || !reflect.DeepEqual(values, []interface{}{nil, "literal NULL"}) {
		t.Fatalf("NULL 必须保持语义，普通文本仍须绑定: query=%s values=%v", query, values)
	}
}
