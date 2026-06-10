package builder

import "testing"

// TestRebindDialects 验证各方言把统一的 ? 占位符转换为正确的占位符风格。
func TestRebindDialects(t *testing.T) {
	cases := []struct {
		name    string
		builder Builder
		in      string
		want    string
	}{
		{"mysql", &Mysql{}, "SELECT * FROM t WHERE a = ? AND b = ?", "SELECT * FROM t WHERE a = ? AND b = ?"},
		{"sqlite", &Sqlite{}, "SELECT * FROM t WHERE a = ?", "SELECT * FROM t WHERE a = ?"},
		{"pgsql", &Pgsql{}, "UPDATE t SET a = ? WHERE id = ?", "UPDATE t SET a = $1 WHERE id = $2"},
		{"sqlsrv", &Sqlsrv{}, "UPDATE t SET a = ? WHERE id = ?", "UPDATE t SET a = @p1 WHERE id = @p2"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.builder.Rebind(c.in); got != c.want {
				t.Fatalf("%s Rebind = %q, want %q", c.name, got, c.want)
			}
		})
	}
}

// Builder 接口在本包内定义副本，仅用于测试约束。
type Builder interface {
	Rebind(query string) string
}

// TestPgsqlUpdateConsistentPlaceholders 验证 PostgreSQL 的 SET 与 WHERE
// 经 Rebind 后占位符风格一致，不再出现 $N 与 ? 混用。
func TestPgsqlUpdateConsistentPlaceholders(t *testing.T) {
	b := &Pgsql{}
	query, _ := b.Update("users", map[string]interface{}{"name": "x"}, []string{"id = ?"})
	final := b.Rebind(query)
	want := `UPDATE "users" SET "name" = $1 WHERE id = $2`
	if final != want {
		t.Fatalf("最终 SQL = %q, want %q", final, want)
	}
}
