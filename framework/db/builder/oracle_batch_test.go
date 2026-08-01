//go:build oracle
// +build oracle

package builder

import (
	"errors"
	"reflect"
	"testing"

	"thinkgo/framework/db"
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
	wantSQL := "INSERT ALL INTO \"USERS\" (\"EMAIL\", \"NAME\") VALUES (:1, :2) " +
		"INTO \"USERS\" (\"EMAIL\", \"NAME\") VALUES (:3, :4) SELECT 1 FROM DUAL"
	if got := oracle.Rebind(query); got != wantSQL {
		t.Fatalf("Oracle 批量插入 SQL 错误:\n got %q\nwant %q", got, wantSQL)
	}
	wantValues := []interface{}{"alice@example.com", "alice", "bob@example.com", "bob"}
	if !reflect.DeepEqual(values, wantValues) {
		t.Fatalf("Oracle 批量插入参数顺序错误: got=%#v want=%#v", values, wantValues)
	}
}
