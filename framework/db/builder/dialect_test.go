package builder

import "testing"

// TestPaginationDialects 验证各方言生成正确的分页语法。
func TestPaginationDialects(t *testing.T) {
	t.Run("mysql", func(t *testing.T) {
		order, limit := (&Mysql{}).Pagination("id DESC", 10, 20)
		if order != " ORDER BY id DESC" || limit != " LIMIT 10 OFFSET 20" {
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
		if order != " ORDER BY name ASC" || limit != "" {
			t.Fatalf("SQL Server 无分页错误: order=%q limit=%q", order, limit)
		}
	})
}

// TestLockClauseDialects 验证悲观锁子句方言差异。
func TestLockClauseDialects(t *testing.T) {
	if got := (&Mysql{}).LockClause("FOR UPDATE"); got != " FOR UPDATE" {
		t.Fatalf("mysql FOR UPDATE: %q", got)
	}
	if got := (&Pgsql{}).LockClause("LOCK IN SHARE MODE"); got != " FOR SHARE" {
		t.Fatalf("pgsql 共享锁应译为 FOR SHARE，实际 %q", got)
	}
	if got := (&Sqlite{}).LockClause("FOR UPDATE"); got != "" {
		t.Fatalf("sqlite 不应输出锁子句，实际 %q", got)
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
