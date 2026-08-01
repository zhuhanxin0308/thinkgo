package framework

import (
	"testing"

	"thinkgo/framework/db"
	"thinkgo/framework/env"
)

// TestApplyDatabaseEnvOverridesReadsPoolSettings 验证数据库连接池参数可从环境变量覆盖。
func TestApplyDatabaseEnvOverridesReadsPoolSettings(t *testing.T) {
	t.Setenv("DB_TYPE", "mysql")
	t.Setenv("DB_HOST", "10.0.0.8")
	t.Setenv("DB_PORT", "3307")
	t.Setenv("DB_USER", "tester")
	t.Setenv("DB_PASS", "secret")
	t.Setenv("DB_NAME", "demo")
	t.Setenv("DB_MAX_OPEN_CONNS", "64")
	t.Setenv("DB_MAX_IDLE_CONNS", "16")
	t.Setenv("DB_CONN_MAX_LIFETIME_SECONDS", "180")
	t.Setenv("DB_CONN_MAX_IDLE_TIME_SECONDS", "45")
	t.Setenv("DB_TIMESTAMP_VALUE_TYPE", "unix")

	app := &App{env: env.NewEnv()}
	config := db.Config{}
	if err := applyDatabaseEnvOverrides(app, &config); err != nil {
		t.Fatalf("应用数据库环境变量失败: %v", err)
	}

	if config.Type != "mysql" || config.Hostname != "10.0.0.8" || config.Hostport != "3307" {
		t.Fatalf("数据库基础连接参数覆盖错误，实际为 %#v", config)
	}
	if config.MaxOpenConns != 64 || config.MaxIdleConns != 16 || config.ConnMaxLifetimeSeconds != 180 || config.ConnMaxIdleTimeSeconds != 45 {
		t.Fatalf("数据库连接池参数覆盖错误，实际为 %#v", config)
	}
	if config.TimestampValueType != db.TimestampValueTypeUnix {
		t.Fatalf("时间戳值类型环境变量覆盖错误，实际为 %q", config.TimestampValueType)
	}
}

// TestApplyDatabaseEnvOverridesAllowsEmptyPassword 验证显式空密码也能覆盖配置文件密码。
func TestApplyDatabaseEnvOverridesAllowsEmptyPassword(t *testing.T) {
	t.Setenv("DB_PASS", "")

	app := &App{env: env.NewEnv()}
	config := db.Config{Password: "from-config"}
	if err := applyDatabaseEnvOverrides(app, &config); err != nil {
		t.Fatalf("应用空密码环境变量失败: %v", err)
	}

	if config.Password != "" {
		t.Fatalf("显式空 DB_PASS 应覆盖配置文件密码，实际为 %q", config.Password)
	}
}

// TestReadDatabaseConfigReadsTimestampValueType 验证数据库配置文件中的时间戳值类型能正确读入。
func TestReadDatabaseConfigReadsTimestampValueType(t *testing.T) {
	config, err := readDatabaseConfig(map[string]interface{}{
		"type":                 "mysql",
		"auto_timestamp":       true,
		"timestamp_value_type": "unix",
	})
	if err != nil {
		t.Fatalf("读取数据库配置失败: %v", err)
	}

	if config.TimestampValueType != db.TimestampValueTypeUnix {
		t.Fatalf("时间戳值类型读取错误，实际为 %q", config.TimestampValueType)
	}
}

// TestReadDatabaseConfigReadsConnectionParams 验证数据库连接参数会从配置文件进入连接器。
func TestReadDatabaseConfigReadsConnectionParams(t *testing.T) {
	config, err := readDatabaseConfig(map[string]interface{}{
		"type": "pgsql",
		"params": map[string]interface{}{
			"sslmode":         "verify-full",
			"connect_timeout": "10",
			"encrypt":         "true",
		},
	})
	if err != nil {
		t.Fatalf("读取数据库参数失败: %v", err)
	}

	if config.Params["sslmode"] != "verify-full" {
		t.Fatalf("数据库连接参数 sslmode 读取错误，实际为 %q", config.Params["sslmode"])
	}
	if config.Params["connect_timeout"] != "10" || config.Params["encrypt"] != "true" {
		t.Fatalf("数据库连接参数类型转换错误，实际为 %#v", config.Params)
	}
}

// TestApplyDatabaseFallbacksUsesUnixTimestampValueTypeByDefault 验证数据库配置缺省时仍回退到 Unix 秒模式，
// 避免运行环境漏配后重新写入 datetime 字符串。
func TestApplyDatabaseFallbacksUsesUnixTimestampValueTypeByDefault(t *testing.T) {
	config := db.Config{}
	applyDatabaseFallbacks(&config)

	if config.TimestampValueType != db.TimestampValueTypeUnix {
		t.Fatalf("默认时间戳值类型应为 unix，实际为 %q", config.TimestampValueType)
	}
}
