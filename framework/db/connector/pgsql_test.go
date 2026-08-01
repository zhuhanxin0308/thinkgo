package connector

import (
	"net/url"
	"testing"

	"thinkgo/framework/db"
)

// TestBuildPgsqlDSNEscapesCredentialsAndRequiresTLS 验证 PostgreSQL DSN 不能被特殊字符注入，并默认启用 TLS。
func TestBuildPgsqlDSNEscapesCredentialsAndVerifiesTLS(t *testing.T) {
	dsn := buildPgsqlDSN(db.Config{
		Username: "pg:user",
		Password: "p@ss word&sslmode=disable",
		Hostname: "db.internal",
		Hostport: "5432",
		Database: "think go",
	})

	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("PostgreSQL DSN 应为可解析 URL，实际错误: %v", err)
	}
	password, _ := parsed.User.Password()
	if parsed.User.Username() != "pg:user" || password != "p@ss word&sslmode=disable" {
		t.Fatalf("PostgreSQL DSN 凭据解析错误: user=%q password=%q", parsed.User.Username(), password)
	}
	if parsed.Host != "db.internal:5432" || parsed.EscapedPath() != "/think%20go" {
		t.Fatalf("PostgreSQL DSN 地址或库名解析错误: host=%q path=%q", parsed.Host, parsed.EscapedPath())
	}
	if parsed.Query().Get("sslmode") != "verify-full" {
		t.Fatalf("PostgreSQL DSN 默认应启用 sslmode=verify-full，实际为 %q", parsed.Query().Get("sslmode"))
	}
}

// TestBuildPgsqlDSNAllowsExplicitTLSOverride 验证本地开发可通过参数显式覆盖 TLS 模式。
func TestBuildPgsqlDSNAllowsExplicitTLSOverride(t *testing.T) {
	dsn := buildPgsqlDSN(db.Config{
		Hostname: "127.0.0.1",
		Hostport: "5432",
		Database: "thinkgo",
		Params: map[string]string{
			"sslmode": "disable",
		},
	})

	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("PostgreSQL DSN 应为可解析 URL，实际错误: %v", err)
	}
	if parsed.Query().Get("sslmode") != "disable" {
		t.Fatalf("PostgreSQL DSN 应尊重显式 sslmode 覆盖，实际为 %q", parsed.Query().Get("sslmode"))
	}
}
