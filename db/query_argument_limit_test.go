package db

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework/db/builder"
)

// TestQueryWhereInRejectsArgumentsAboveFallbackLimit 验证无 SQL 方言连接时的统一参数上限。
func TestQueryWhereInRejectsArgumentsAboveFallbackLimit(t *testing.T) {
	database := NewDB(&mockConnection{})
	values := make([]interface{}, maxQueryArguments+1)

	query := database.Table("users").WhereIn("id", values)
	if !errors.Is(query.err, ErrQueryArgumentsTooMany) {
		t.Fatalf("超出统一参数上限应返回 ErrQueryArgumentsTooMany，实际为 %v", query.err)
	}
	if len(query.where) != 0 || len(query.args) != 0 {
		t.Fatalf("参数超限时不应写入条件或参数，where=%v args=%d", query.where, len(query.args))
	}
}

// TestSQLConnectionRejectsDialectArgumentOverflow 验证底层 SQL 连接也执行方言参数预算。
func TestSQLConnectionRejectsDialectArgumentOverflow(t *testing.T) {
	connection := newHardeningSQLConnection(t)
	args := make([]interface{}, (&builder.Sqlite{}).MaxBindParams()+1)
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")

	_, err := connection.Query("SELECT "+placeholders, args...)
	if !errors.Is(err, ErrQueryArgumentsTooMany) {
		t.Fatalf("底层 SQL 连接超出方言预算应返回 ErrQueryArgumentsTooMany，实际为 %v", err)
	}
}

// TestDBRawQueryRejectsArgumentOverflow 验证 DB 原生入口不会绕过统一参数预算。
func TestDBRawQueryRejectsArgumentOverflow(t *testing.T) {
	connection := &recordingRawConn{}
	database := NewDB(connection)
	args := make([]interface{}, maxQueryArguments+1)
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")

	_, err := database.Query("SELECT "+placeholders, args...)
	if !errors.Is(err, ErrQueryArgumentsTooMany) {
		t.Fatalf("DB 原生入口超出统一预算应返回 ErrQueryArgumentsTooMany，实际为 %v", err)
	}
	if connection.lastQuery != "" {
		t.Fatalf("参数超限时不应提交给驱动，实际 SQL 为 %q", connection.lastQuery)
	}
}

// TestTransactionWriteRejectsArgumentOverflow 验证事务内直接执行路径也遵守参数预算。
func TestTransactionWriteRejectsArgumentOverflow(t *testing.T) {
	connection := newHardeningSQLConnection(t)
	database := NewDB(connection)
	transaction, err := database.Begin()
	if err != nil {
		t.Fatalf("创建测试事务失败: %v", err)
	}
	t.Cleanup(func() { _ = transaction.Rollback() })

	data := make(map[string]interface{}, (&builder.Sqlite{}).MaxBindParams()+1)
	for index := 0; index < (&builder.Sqlite{}).MaxBindParams()+1; index++ {
		data[fmt.Sprintf("field_%d", index)] = index
	}
	_, err = transaction.Name("users").WhereField("id", "=", 1).Update(data)
	if !errors.Is(err, ErrQueryArgumentsTooMany) {
		t.Fatalf("事务写入超出方言预算应返回 ErrQueryArgumentsTooMany，实际为 %v", err)
	}
}

// TestQueryArgumentLimitUsesDialectBudget 验证 SQL 方言预算优先于通用回退上限。
func TestQueryArgumentLimitUsesDialectBudget(t *testing.T) {
	database := NewDB(&SQLConnection{Builder: &builder.Sqlite{}})
	values := make([]interface{}, (&builder.Sqlite{}).MaxBindParams()+1)

	query := database.Table("users").WhereIn("id", values)
	if !errors.Is(query.err, ErrQueryArgumentsTooMany) {
		t.Fatalf("超出 SQLite 方言参数预算应返回 ErrQueryArgumentsTooMany，实际为 %v", query.err)
	}
	if len(query.where) != 0 || len(query.args) != 0 {
		t.Fatalf("方言参数超限时不应写入条件或参数，where=%v args=%d", query.where, len(query.args))
	}
}

// TestQueryArgumentLimitCoversWhereAndHaving 验证 WHERE 与 HAVING 共享同一条参数预算。
func TestQueryArgumentLimitCoversWhereAndHaving(t *testing.T) {
	database := NewDB(&mockConnection{})
	first := maxQueryArguments

	query := database.Table("users").WhereIn("id", make([]interface{}, first))
	query = query.HavingRaw("COUNT(id) > ?", 1)
	if !errors.Is(query.err, ErrQueryArgumentsTooMany) {
		t.Fatalf("WHERE 与 HAVING 合计超限应返回 ErrQueryArgumentsTooMany，实际为 %v", query.err)
	}
	if len(query.havingArgs) != 0 {
		t.Fatalf("HAVING 超限时不应写入参数，实际为 %d", len(query.havingArgs))
	}
}
