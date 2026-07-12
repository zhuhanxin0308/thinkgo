package connector

import (
	"strings"
	"testing"
	"time"

	"thinkgo/framework/db"

	mysqlDriver "github.com/go-sql-driver/mysql"
)

// TestResolveMysqlConnectionPoolConfigUsesDefaults 验证未显式配置时仍使用安全默认连接池参数。
func TestResolveMysqlConnectionPoolConfigUsesDefaults(t *testing.T) {
	settings := resolveMysqlConnectionPoolConfig(db.Config{})
	if settings.MaxOpenConns != defaultMaxOpenConns {
		t.Fatalf("默认最大连接数错误，期望 %d，实际为 %d", defaultMaxOpenConns, settings.MaxOpenConns)
	}
	if settings.MaxIdleConns != defaultMaxIdleConns {
		t.Fatalf("默认最大空闲连接数错误，期望 %d，实际为 %d", defaultMaxIdleConns, settings.MaxIdleConns)
	}
	if settings.ConnMaxLifetime != defaultConnMaxLifetime {
		t.Fatalf("默认连接生命周期错误，期望 %s，实际为 %s", defaultConnMaxLifetime, settings.ConnMaxLifetime)
	}
	if settings.ConnMaxIdleTime != defaultConnMaxIdleTime {
		t.Fatalf("默认连接空闲时长错误，期望 %s，实际为 %s", defaultConnMaxIdleTime, settings.ConnMaxIdleTime)
	}
}

// TestResolveMysqlConnectionPoolConfigUsesOverrides 验证显式配置会覆盖默认连接池参数。
func TestResolveMysqlConnectionPoolConfigUsesOverrides(t *testing.T) {
	settings := resolveMysqlConnectionPoolConfig(db.Config{
		MaxOpenConns:           80,
		MaxIdleConns:           20,
		ConnMaxLifetimeSeconds: 90,
		ConnMaxIdleTimeSeconds: 45,
	})
	if settings.MaxOpenConns != 80 {
		t.Fatalf("最大连接数覆盖失败，实际为 %d", settings.MaxOpenConns)
	}
	if settings.MaxIdleConns != 20 {
		t.Fatalf("最大空闲连接数覆盖失败，实际为 %d", settings.MaxIdleConns)
	}
	if settings.ConnMaxLifetime != 90*time.Second {
		t.Fatalf("连接生命周期覆盖失败，实际为 %s", settings.ConnMaxLifetime)
	}
	if settings.ConnMaxIdleTime != 45*time.Second {
		t.Fatalf("连接空闲时长覆盖失败，实际为 %s", settings.ConnMaxIdleTime)
	}
}

// TestBuildMysqlDSNIncludesHealthCheckParameters 验证默认 DSN 会带上连接健康检查参数，避免长期空闲后继续复用脏连接。
func TestBuildMysqlDSNIncludesHealthCheckParameters(t *testing.T) {
	dsn := buildMysqlDSN(db.Config{
		Username: "root",
		Password: "secret",
		Hostname: "127.0.0.1",
		Hostport: "3306",
		Database: "thinkgo",
	})

	expectedFragments := []string{
		"checkConnLiveness=true",
		"parseTime=True",
		"loc=Local",
		"charset=utf8mb4",
		"tls=true",
	}
	for _, fragment := range expectedFragments {
		if !strings.Contains(dsn, fragment) {
			t.Fatalf("DSN 应包含连接健康参数 %q，实际为 %s", fragment, dsn)
		}
	}
}

// TestBuildMysqlDSNEscapesCredentials 验证 DSN 构造不会被用户名、密码和库名中的特殊字符破坏。
func TestBuildMysqlDSNEscapesCredentials(t *testing.T) {
	dsn := buildMysqlDSN(db.Config{
		Username: "root@example",
		Password: "p@ss:word/with?x",
		Hostname: "127.0.0.1",
		Hostport: "3306",
		Database: "think go",
	})

	parsed, err := mysqlDriver.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("MySQL DSN 应可被官方驱动解析，实际错误: %v，DSN: %s", err, dsn)
	}
	if parsed.User != "root@example" || parsed.Passwd != "p@ss:word/with?x" || parsed.DBName != "think go" {
		t.Fatalf("MySQL DSN 凭据或库名解析错误: user=%q password=%q database=%q", parsed.User, parsed.Passwd, parsed.DBName)
	}
	if parsed.Addr != "127.0.0.1:3306" {
		t.Fatalf("MySQL DSN 地址解析错误，实际为 %q", parsed.Addr)
	}
}
