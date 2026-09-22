package builder

import (
	"errors"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

// TestPaginationDialects 验证各方言生成正确的分页语法。
func TestPaginationDialects(t *testing.T) {
	t.Run("mysql", func(t *testing.T) {
		order, limit := (&Mysql{}).Pagination("id DESC", 10, 20)
		if order != " ORDER BY `id` DESC" || limit != " LIMIT 10 OFFSET 20" {
			t.Fatalf("mysql 分页错误: order=%q limit=%q", order, limit)
		}
	})
	t.Run("pgsql", func(t *testing.T) {
		order, limit := (&Pgsql{}).Pagination("", 5, 0)
		if order != "" || limit != " LIMIT 5" {
			t.Fatalf("pgsql 分页错误: order=%q limit=%q", order, limit)
		}
	})
	t.Run("sqlsrv_requires_order", func(t *testing.T) {
		order, limit := (&Sqlsrv{}).Pagination("", 10, 20)
		if order != " ORDER BY (SELECT NULL)" {
			t.Fatalf("SQL Server 分页缺省排序错误: %q", order)
		}
		if limit != " OFFSET 20 ROWS FETCH NEXT 10 ROWS ONLY" {
			t.Fatalf("SQL Server 分页子句错误: %q", limit)
		}
	})
	t.Run("sqlsrv_no_paging", func(t *testing.T) {
		order, limit := (&Sqlsrv{}).Pagination("name ASC", 0, 0)
		if order != " ORDER BY [name] ASC" || limit != "" {
			t.Fatalf("SQL Server 无分页错误: order=%q limit=%q", order, limit)
		}
	})
}

func TestTypedLockCapabilities(t *testing.T) {
	tests := []struct {
		name    string
		builder db.Builder
		mode    db.LockMode
		tail    string
		table   string
		wantErr error
	}{
		{name: "mysql update", builder: &Mysql{}, mode: db.LockForUpdate, tail: " FOR UPDATE"},
		{name: "mysql share", builder: &Mysql{}, mode: db.LockForShare, tail: " LOCK IN SHARE MODE"},
		{name: "postgres share", builder: &Pgsql{}, mode: db.LockForShare, tail: " FOR SHARE"},
		{name: "sqlite update", builder: &Sqlite{}, mode: db.LockForUpdate, wantErr: db.ErrUnsupportedFeature},
		{name: "sqlserver update", builder: &Sqlsrv{}, mode: db.LockForUpdate, table: " WITH (UPDLOCK, ROWLOCK)"},
		{name: "sqlserver share", builder: &Sqlsrv{}, mode: db.LockForShare, table: " WITH (HOLDLOCK, ROWLOCK)"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			spec, err := testCase.builder.Lock(testCase.mode)
			if !errors.Is(err, testCase.wantErr) || spec.Tail != testCase.tail || spec.TableHint != testCase.table {
				t.Fatalf("lock capability mismatch: spec=%#v err=%v", spec, err)
			}
		})
	}
}

// TestPgsqlInsertReturning 验证 PostgreSQL 使用 RETURNING 回传主键。
func TestPgsqlInsertReturning(t *testing.T) {
	p := &Pgsql{}
	if p.SupportsLastInsertId() {
		t.Fatal("PostgreSQL 不应声明支持 LastInsertId")
	}
	query, values, ok := p.InsertReturning("users", map[string]interface{}{"name": "x"}, "id")
	if !ok {
		t.Fatal("PostgreSQL 应支持 InsertReturning")
	}
	if len(values) != 1 {
		t.Fatalf("values 数量错误: %d", len(values))
	}
	final := p.Rebind(query)
	want := `INSERT INTO "users" ("name") VALUES ($1) RETURNING "id"`
	if final != want {
		t.Fatalf("PostgreSQL RETURNING SQL 错误:\n got %q\nwant %q", final, want)
	}
}

// TestMysqlSupportsLastInsertId 验证 MySQL 仍走 LastInsertId 路径。
func TestMysqlSupportsLastInsertId(t *testing.T) {
	if !(&Mysql{}).SupportsLastInsertId() {
		t.Fatal("MySQL 应支持 LastInsertId")
	}
	if _, _, ok := (&Mysql{}).InsertReturning("t", nil, "id"); ok {
		t.Fatal("MySQL 不应使用 InsertReturning")
	}
}

// TestSqlsrvInsertReturningUsesOutputInserted 验证 SQL Server 不依赖 LastInsertId，
// 而是生成可由 QueryRow 扫描的 OUTPUT INSERTED 主键回传语句。
func TestSqlsrvInsertReturningUsesOutputInserted(t *testing.T) {
	s := &Sqlsrv{}
	if s.SupportsLastInsertId() {
		t.Fatal("SQL Server 不应声明支持 LastInsertId")
	}
	query, values, ok := s.InsertReturning("users", map[string]interface{}{"name": "x"}, "id")
	if !ok {
		t.Fatal("SQL Server 应支持 InsertReturning")
	}
	if len(values) != 1 {
		t.Fatalf("values 数量错误: %d", len(values))
	}
	final := s.Rebind(query)
	want := "INSERT INTO [users] ([name]) OUTPUT INSERTED.[id] VALUES (@p1)"
	if final != want {
		t.Fatalf("SQL Server OUTPUT INSERTED SQL 错误:\n got %q\nwant %q", final, want)
	}
}

// TestQuoteMultipartIdentifier 验证多级点分标识符会逐段引用，
// 避免 schema/table/column 被错误合并成单段名称。
func TestQuoteMultipartIdentifier(t *testing.T) {
	if got := (&Sqlsrv{}).QuoteIdentifier("catalog.dbo.users"); got != "[catalog].[dbo].[users]" {
		t.Fatalf("SQL Server 多级标识符引用错误: %q", got)
	}
	if got := (&Pgsql{}).QuoteIdentifier("public.audit.logs"); got != `"public"."audit"."logs"` {
		t.Fatalf("PostgreSQL 多级标识符引用错误: %q", got)
	}
}
