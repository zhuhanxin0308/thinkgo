package connector

import (
	"errors"
	"strings"
	"testing"
	"time"

	"thinkgo/framework/db"

	mysqlDriver "github.com/go-sql-driver/mysql"
)

func validMysqlConfig() db.Config {
	return db.Config{
		Type:     "mysql",
		Username: "root",
		Password: "secret",
		Hostname: "127.0.0.1",
		Hostport: "3306",
		Database: "thinkgo",
	}
}

func mysqlConfigWithParams(params map[string]string) db.Config {
	config := validMysqlConfig()
	config.Params = params
	return config
}

func TestMysqlRejectsEveryDangerousTrueValue(t *testing.T) {
	for _, value := range []string{"1", "t", "T", "TRUE", "True", "true"} {
		for _, key := range []string{"multiStatements", "allowAllFiles", "allowCleartextPasswords", "allowFallbackToPlaintext", "allowOldPasswords"} {
			if _, err := buildMysqlDSN(mysqlConfigWithParams(map[string]string{key: value})); !errors.Is(err, db.ErrInvalidDatabaseConfig) {
				t.Fatalf("dangerous MySQL parameter %s=%s was not rejected: %v", key, value, err)
			}
		}
	}
}

func TestMysqlRejectsCanonicalKeyDuplicatesAndColonUsername(t *testing.T) {
	if _, err := buildMysqlDSN(mysqlConfigWithParams(map[string]string{
		"multiStatements": "false",
		"MULTISTATEMENTS": "false",
	})); !errors.Is(err, db.ErrInvalidDatabaseConfig) {
		t.Fatalf("canonical duplicate MySQL parameters must be rejected: %v", err)
	}
	config := validMysqlConfig()
	config.Username = "tenant:admin"
	if _, err := buildMysqlDSN(config); !errors.Is(err, db.ErrInvalidDatabaseConfig) {
		t.Fatalf("MySQL username containing colon must be rejected: %v", err)
	}
	if _, err := buildMysqlDSN(mysqlConfigWithParams(map[string]string{"safe&multiStatements": "true"})); !errors.Is(err, db.ErrInvalidDatabaseConfig) {
		t.Fatalf("structural characters in MySQL parameter keys must be rejected: %v", err)
	}
}

func TestMysqlTypedParametersOverrideDefaults(t *testing.T) {
	config := mysqlConfigWithParams(map[string]string{
		"TLS":               "false",
		"parseTime":         "false",
		"checkConnLiveness": "false",
		"clientFoundRows":   "true",
		"loc":               "UTC",
		"maxAllowedPacket":  "4096",
	})
	dsn, err := buildMysqlDSN(config)
	if err != nil {
		t.Fatalf("build typed MySQL parameters: %v", err)
	}
	parsed, err := mysqlDriver.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("parse typed MySQL parameters: %v", err)
	}
	if parsed.TLSConfig != "false" || parsed.ParseTime || parsed.CheckConnLiveness || !parsed.ClientFoundRows || parsed.Loc != time.UTC || parsed.MaxAllowedPacket != 4096 {
		t.Fatalf("typed MySQL overrides mismatch: %#v", parsed)
	}
}

// TestMysqlRejectsNegativeMaxAllowedPacket 验证包大小不能以负数绕过连接和批量写入校验。
func TestMysqlRejectsNegativeMaxAllowedPacket(t *testing.T) {
	if _, err := buildMysqlDSN(mysqlConfigWithParams(map[string]string{"maxAllowedPacket": "-1"})); !errors.Is(err, db.ErrInvalidDatabaseConfig) {
		t.Fatalf("负数 maxAllowedPacket 应返回配置错误，实际为 %v", err)
	}
}

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
	dsn, err := buildMysqlDSN(db.Config{
		Username: "root",
		Password: "secret",
		Hostname: "127.0.0.1",
		Hostport: "3306",
		Database: "thinkgo",
	})
	if err != nil {
		t.Fatalf("build MySQL DSN: %v", err)
	}

	expectedFragments := []string{
		"parseTime=true",
		"loc=Local",
		"charset=utf8mb4",
		"tls=true",
	}
	for _, fragment := range expectedFragments {
		if !strings.Contains(dsn, fragment) {
			t.Fatalf("DSN 应包含连接健康参数 %q，实际为 %s", fragment, dsn)
		}
	}
	parsed, err := mysqlDriver.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("parse typed MySQL DSN: %v", err)
	}
	if !parsed.CheckConnLiveness || !parsed.ParseTime || parsed.Loc != time.Local || parsed.TLSConfig != "true" {
		t.Fatalf("typed MySQL safety defaults missing: %#v", parsed)
	}
}

// TestBuildMysqlDSNEscapesCredentials 验证 DSN 构造不会被用户名、密码和库名中的特殊字符破坏。
func TestBuildMysqlDSNEscapesCredentials(t *testing.T) {
	dsn, buildErr := buildMysqlDSN(db.Config{
		Username: "root@example",
		Password: "p@ss:word/with?x",
		Hostname: "127.0.0.1",
		Hostport: "3306",
		Database: "think go",
	})
	if buildErr != nil {
		t.Fatalf("build MySQL DSN: %v", buildErr)
	}

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
