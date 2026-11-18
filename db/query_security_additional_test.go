package db

import (
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
)

// TestWhereRejectsUnsafeRawCondition 验证默认 Where 不再接受拼接出来的原始 SQL。
func TestWhereRejectsUnsafeRawCondition(t *testing.T) {
	db := NewDB(&mockConnection{})

	_, err := db.Table("users").Where("name = 'admin' OR 1=1 --").Select()
	if err == nil {
		t.Fatal("默认 Where 应拒绝包含拼接值的原始 SQL 条件")
	}
}

// TestWhereAllowsParameterizedCondition 验证默认 Where 仍然支持参数化条件。
func TestWhereAllowsParameterizedCondition(t *testing.T) {
	db := NewDB(&mockConnection{})

	_, err := db.Table("users").Where("name = ?", "admin").Select()
	if err != nil {
		t.Fatalf("参数化 Where 条件应正常通过，实际错误: %v", err)
	}
}

// TestWhereAllowsIdentifierShorthand 验证默认 Where 兼容旧版 field,value 速记写法。
func TestWhereAllowsIdentifierShorthand(t *testing.T) {
	db := NewDB(&mockConnection{})

	q := db.Table("users").Where("id", 1)
	if q.err != nil {
		t.Fatalf("field,value 速记写法应被规范化为安全条件，实际错误: %v", q.err)
	}
	if len(q.where) != 1 || q.where[0] != "id = ?" {
		t.Fatalf("速记写法应被转换为 'id = ?'，实际 where=%v", q.where)
	}
}

// TestWhereAllowsNullPredicate 验证默认 Where 支持安全的 NULL 谓词。
func TestWhereAllowsNullPredicate(t *testing.T) {
	db := NewDB(&mockConnection{})

	_, err := db.Table("users").Where("delete_time IS NULL").Select()
	if err != nil {
		t.Fatalf("IS NULL 条件应正常通过，实际错误: %v", err)
	}
}

// TestHavingRejectsUnsafeRawCondition 验证默认 Having 不再接受拼接式原始 SQL。
func TestHavingRejectsUnsafeRawCondition(t *testing.T) {
	db := NewDB(&mockConnection{})

	q := db.Table("users").Group("status").Having("total > 1; DROP TABLE users")
	if q.err == nil {
		t.Fatal("默认 Having 应拒绝包含注入片段的原始 SQL")
	}
}

// TestHavingAllowsParameterizedCondition 验证默认 Having 支持参数化安全条件。
func TestHavingAllowsParameterizedCondition(t *testing.T) {
	db := NewDB(&mockConnection{})

	q := db.Table("users").Group("status").Having("total >= ?", 2)
	if q.err != nil {
		t.Fatalf("参数化 Having 条件应正常通过，实际错误: %v", q.err)
	}
	if q.having != "total >= ?" {
		t.Fatalf("Having 条件格式不正确，实际为 %q", q.having)
	}
}

// TestHavingRawKeepsExplicitRawEntry 验证显式 raw API 仍可保留复杂 HAVING 表达式。
func TestHavingRawKeepsExplicitRawEntry(t *testing.T) {
	db := NewDB(&mockConnection{})

	q := db.Table("users").Group("status").HavingRaw("COUNT(id) > ?", 1)
	if q.err != nil {
		t.Fatalf("HavingRaw 应保留显式原始 SQL 入口，实际错误: %v", q.err)
	}
	if q.having != "COUNT(id) > ?" {
		t.Fatalf("HavingRaw 应保留原始条件，实际为 %q", q.having)
	}
}

// TestRawClausesRejectStatementSeparators 验证公开条件入口拒绝 SQL 正文中的语句分隔符。
func TestRawClausesRejectStatementSeparators(t *testing.T) {
	database := NewDB(newHardeningSQLConnection(t))
	for _, query := range []*Query{
		database.Table("users").WhereRaw("id = ?; SELECT 1", 1),
		database.Table("users").Group("status").HavingRaw("COUNT(id) > ?; SELECT 1", 1),
	} {
		if _, _, err := query.BuildSelectSQL(); err == nil || !strings.Contains(err.Error(), "语句分隔符") {
			t.Errorf("原始条件应拒绝 SQL 语句分隔符: %v", err)
		}
	}
}

// TestRawClauseSeparatorsHonorDialectBoundaries 验证引号、注释及不同方言的词法边界。
func TestRawClauseSeparatorsHonorDialectBoundaries(t *testing.T) {
	for _, sample := range []struct {
		dialect, condition string
		args               int
		valid              bool
	}{
		{"sqlite", "[a;b] = ? AND name <> 'x;y'", 1, true},
		{"sqlite", "id = ? -- ; 上方是唯一有效条件", 1, true},
		{"sqlite", "id = ? -- SQLite 行注释\r; 仍在注释内\nAND id > 0", 1, true},
		{"sqlite", "id = ? AND EXISTS(SELECT 1 AS a$x$); DELETE FROM audit; SELECT 1 AS a$x$", 1, false},
		{"postgres", "id = ? AND name <> $tag$x;y$tag$", 1, true},
		{"postgres", "id = ? /* outer /* inner ; */ more ; */", 1, true},
		{"postgres", "id = ? AND a `#` ?", 2, true},
		{"sqlserver", "id = ? /* outer /* inner ; */ more ; */", 1, true},
		{"oracle", "id = ? AND name <> q'[x';y]'", 1, true},
		{"oracle", "id = ? AND name <> nq'<x';y>'", 1, true},
		{"oracle", "id = ? AND name <> q'[x';y]'; SELECT 1 FROM dual", 1, false},
		{"mysql", "id = ? # ; 行注释", 1, true},
		{"mysql", "id = ? # MySQL 行注释\r; 仍在注释内\nAND id > 0", 1, true},
		{"mysql", "id = ? -- MySQL 行注释\r; 仍在注释内\nAND id > 0", 1, true},
		{"mysql", "id = ? -- 这里是注释 ;", 1, true},
		{"mysql", "id = ?--0; SELECT 1", 1, false},
		{"mysql", "EXISTS(SELECT 1 AS `a\\`); DELETE FROM audit; -- `", 0, false},
		{"mysql", "name = 'end\\'; DELETE FROM audit; -- '", 0, false},
		{"mysql", "name = \"end\\\"; DELETE FROM audit; -- \"", 0, false},
		{"mysql", "id = ? AND name = 'it''s ; fine' AND dir = 'C:\\\\'", 1, true},
		{"mysql", "id = ? AND `a``;b` = ?", 2, true},
		{"mysql", "id = ? /*! ; SELECT 1 */", 1, false},
		{"mysql", "id = ? /*M! ; SELECT 1 */", 1, false},
		{"mysql", "id = ? /*m! ; SELECT 1 */", 1, false},
		{"mysql", "id = ? /*! AND name <> 'x;y' */", 1, true},
	} {
		err := validateRawClause(sample.condition, sample.args, "where", sample.dialect)
		if sample.valid && err != nil || !sample.valid && (err == nil || !strings.Contains(err.Error(), "语句分隔符")) {
			t.Errorf("方言 %s 条件 %q 的校验结果错误: %v", sample.dialect, sample.condition, err)
		}
	}
	if err := validateRawStatement("SELECT 1;", 0, "sqlite"); err != nil {
		t.Fatalf("完整原生 SQL 入口应保留终止符: %v", err)
	}
	if err := validateRawClause("name = 'ambiguous\\' AND id = ?", 1, "where", "mysql"); err == nil {
		t.Fatal("依赖会话转义模式的歧义引号边界应被拒绝")
	}
}

// TestDollarIdentifiersDoNotSwallowPlaceholdersAcrossDialects 验证含 $ 的合法列名与非 PG 方言不会误触发 dollar-quote (#593)。
func TestDollarIdentifiersDoNotSwallowPlaceholdersAcrossDialects(t *testing.T) {
	const query = "SELECT a$t$ FROM v$session WHERE id = ? AND b$t$ = ?"
	for _, dialect := range []string{"oracle", "sqlserver", "mysql", "sqlite", "postgres", ""} {
		count, err := countSQLPlaceholders(query, dialect)
		if err != nil || count != 2 {
			t.Fatalf("方言 %q 计数错误: count=%d err=%v", dialect, count, err)
		}
		redacted := redactSQLLiteralsAndComments(query, dialect)
		if redacted != query {
			t.Fatalf("方言 %q 不应将含 $ 标识符误脱敏: %q", dialect, redacted)
		}
	}
	if got := (&builder.Sqlsrv{}).Rebind(query); got != "SELECT a$t$ FROM v$session WHERE id = @p1 AND b$t$ = @p2" {
		t.Fatalf("SQL Server Rebind 吞没了占位符: %q", got)
	}
	if got := (&builder.Pgsql{}).Rebind(query); got != "SELECT a$t$ FROM v$session WHERE id = $1 AND b$t$ = $2" {
		t.Fatalf("PostgreSQL Rebind 吞没了占位符: %q", got)
	}
	// 非 PostgreSQL 方言即便出现独立 $tag$ 也不应进入 PG dollar-quote 模式。
	const standaloneDollar = "SELECT $tag$, ? FROM t WHERE col = $tag$"
	for _, dialect := range []string{"oracle", "sqlserver", "mysql", "sqlite"} {
		count, err := countSQLPlaceholders(standaloneDollar, dialect)
		if err != nil || count != 1 {
			t.Fatalf("非 PG 方言 %q 不应启用 dollar-quote: count=%d err=%v", dialect, count, err)
		}
	}
	// PostgreSQL 下反引号操作符与 Rebind 一致不进入引用标识符状态。
	const pgBacktick = "SELECT a `#` b, ? FROM items WHERE c = ?"
	if count, err := countSQLPlaceholders(pgBacktick, "postgres"); err != nil || count != 2 {
		t.Fatalf("PostgreSQL 反引号操作符破坏占位符计数: count=%d err=%v", count, err)
	}
	if got := (&builder.Pgsql{}).Rebind(pgBacktick); got != "SELECT a `#` b, $1 FROM items WHERE c = $2" {
		t.Fatalf("PostgreSQL 反引号操作符破坏 Rebind: %q", got)
	}
	if got := redactSQLLiteralsAndComments("SELECT `col\\` # secret\r; still secret\nFROM t WHERE id = ?", "mysql"); strings.Contains(got, "secret") || !strings.Contains(got, "`col\\`") {
		t.Fatalf("MySQL 反引号与 # 行注释脱敏边界错误: %q", got)
	}
}
