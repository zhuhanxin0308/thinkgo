package framework

import (
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

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

// TestReadDatabaseConfigKeepsDiagnosticFlagsIndependent 验证两个数据库诊断开关不会互相覆盖。
func TestReadDatabaseConfigKeepsDiagnosticFlagsIndependent(t *testing.T) {
	config, err := readDatabaseConfig(map[string]interface{}{
		"type":        "mysql",
		"debug":       false,
		"trigger_sql": true,
	})
	if err != nil {
		t.Fatalf("读取数据库诊断配置失败: %v", err)
	}
	if config.Debug {
		t.Fatal("debug=false 不应被 trigger_sql=true 覆盖")
	}
	if !config.TriggerSQL {
		t.Fatal("trigger_sql=true 应独立保留")
	}
}
