//go:build integration

package builder

import (
	"context"
	"database/sql"
	"net"
	"net/url"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

// 只连接显式配置的临时测试服务；执行无副作用的 SELECT，验证服务器实际解析和绑定。
func TestLivePostgresRebindLexicalContract(t *testing.T) {
	host := os.Getenv("THINKGO_LIVE_PGSQL_HOST")
	if host == "" {
		t.Skip("THINKGO_LIVE_PGSQL_HOST is not configured")
	}
	port := os.Getenv("THINKGO_LIVE_PGSQL_PORT")
	if port == "" {
		port = "5432"
	}
	user := os.Getenv("THINKGO_LIVE_PGSQL_USER")
	database := os.Getenv("THINKGO_LIVE_PGSQL_DATABASE")
	if user == "" || database == "" {
		t.Fatal("live PostgreSQL user and database must be explicitly configured")
	}
	connectionURL := &url.URL{
		Scheme: "postgres",
		Host:   net.JoinHostPort(host, port),
		User:   url.UserPassword(user, os.Getenv("THINKGO_LIVE_PGSQL_PASSWORD")),
		Path:   "/" + database,
	}
	options := connectionURL.Query()
	// CI 的临时 PostgreSQL 服务没有 TLS；不复用生产地址或凭据。
	options.Set("sslmode", "disable")
	options.Set("standard_conforming_strings", "on")
	options.Set("connect_timeout", "5")
	options.Set("statement_timeout", "5000")
	connectionURL.RawQuery = options.Encode()
	connection, err := sql.Open("postgres", connectionURL.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := connection.Close(); err != nil {
			t.Error(err)
		}
	})
	connection.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := connection.PingContext(ctx); err != nil {
		t.Fatalf("live PostgreSQL unavailable: %v", err)
	}

	for _, test := range []struct {
		name  string
		query string
		args  []interface{}
		want  string
	}{
		{"array", `SELECT ARRAY[?::integer, ?::integer]::text`, []interface{}{7, 9}, "{7,9}"},
		{"subscript", `SELECT (ARRAY[10,20])[?]::text`, []interface{}{2}, "20"},
		{"slice", `SELECT (ARRAY[10,20,30])[?:?]::text`, []interface{}{1, 2}, "{10,20}"},
		{"standard backslash", `SELECT '\' || ?::text`, []interface{}{"ok"}, `\ok`},
		{"escape string", `SELECT E'it\'s ?' || ?::text`, []interface{}{"ok"}, "it's ?ok"},
		{"escape continuation", "SELECT E'first'\n'it\\'s ?' || ?::text", []interface{}{"ok"}, "firstit's ?ok"},
		{"dollar overlap", `SELECT $$$?$$ || ?::text`, []interface{}{"ok"}, "$?ok"},
		{"unicode tag", `SELECT $文本$ ? $文本$ || ?::text`, []interface{}{"ok"}, " ? ok"},
		{"nested comment", `SELECT /* outer /* inner ? */ still ? */ ?::text`, []interface{}{"ok"}, "ok"},
		{"quoted identifier", `SELECT "c\" FROM (SELECT ?::text AS "c\") AS q`, []interface{}{"ok"}, "ok"},
		{"dollar identifier", `SELECT price$tag$ FROM (SELECT ?::text AS price$tag$) AS q`, []interface{}{"ok"}, "ok"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var got string
			if err := connection.QueryRowContext(ctx, (&Pgsql{}).Rebind(test.query), test.args...).Scan(&got); err != nil {
				t.Fatalf("bound SELECT failed: %v", err)
			}
			if got != test.want {
				t.Fatalf("server result = %q; want %q", got, test.want)
			}
		})
	}
}
