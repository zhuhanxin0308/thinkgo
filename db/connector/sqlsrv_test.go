package connector

import (
	"net/url"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework/db"
)

// TestBuildSqlsrvDSNEscapesCredentialsAndEncrypts 验证 SQL Server DSN 不会被分号注入并默认启用加密。
func TestBuildSqlsrvDSNEscapesCredentialsAndEncrypts(t *testing.T) {
	dsn := buildSqlsrvDSN(db.Config{
		Username: "sa;user",
		Password: "p@ss;encrypt=disable",
		Hostname: "sql.internal",
		Hostport: "1433",
		Database: "think go",
	})

	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("SQL Server DSN 应为可解析 URL，实际错误: %v", err)
	}
	password, _ := parsed.User.Password()
	if parsed.User.Username() != "sa;user" || password != "p@ss;encrypt=disable" {
		t.Fatalf("SQL Server DSN 凭据解析错误: user=%q password=%q", parsed.User.Username(), password)
	}
	if parsed.Host != "sql.internal:1433" {
		t.Fatalf("SQL Server DSN 地址解析错误，实际为 %q", parsed.Host)
	}
	if parsed.Query().Get("database") != "think go" {
		t.Fatalf("SQL Server DSN 库名解析错误，实际为 %q", parsed.Query().Get("database"))
	}
	if parsed.Query().Get("encrypt") != "true" {
		t.Fatalf("SQL Server DSN 默认应启用 encrypt=true，实际为 %q", parsed.Query().Get("encrypt"))
	}
}

// TestBuildSqlsrvDSNAllowsExplicitEncryptOverride 验证测试环境可显式覆盖加密参数。
func TestBuildSqlsrvDSNAllowsExplicitEncryptOverride(t *testing.T) {
	dsn := buildSqlsrvDSN(db.Config{
		Hostname: "127.0.0.1",
		Hostport: "1433",
		Database: "thinkgo",
		Params: map[string]string{
			"encrypt": "disable",
		},
	})

	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("SQL Server DSN 应为可解析 URL，实际错误: %v", err)
	}
	if parsed.Query().Get("encrypt") != "disable" {
		t.Fatalf("SQL Server DSN 应尊重显式 encrypt 覆盖，实际为 %q", parsed.Query().Get("encrypt"))
	}
}
