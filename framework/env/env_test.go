package env

import (
	"os"
	"testing"
)

// TestGetOSEnvOverridesFile 验证真实进程环境变量优先于 .env 文件值（12-factor 约定）。
// 这是 `run -p` 在热重载子进程中生效的关键：os.Setenv 注入的 SERVER_PORT
// 必须能覆盖 .env 里的默认端口。
func TestGetOSEnvOverridesFile(t *testing.T) {
	e := NewEnv()
	e.data["SERVER_PORT"] = "8081" // 模拟 .env 文件中的值

	t.Setenv("SERVER_PORT", "9999")

	if got := e.Get("SERVER_PORT"); got != "9999" {
		t.Fatalf("真实环境变量应优先，期望 9999，得到 %q", got)
	}
}

// TestGetEmptyOSEnvStillOverridesFile 验证显式设置为空的进程环境变量也优先于 .env。
func TestGetEmptyOSEnvStillOverridesFile(t *testing.T) {
	e := NewEnv()
	e.data["SERVER_ALLOWED_HOSTS"] = "example.com"

	t.Setenv("SERVER_ALLOWED_HOSTS", "")

	if got := e.Get("SERVER_ALLOWED_HOSTS", "fallback"); got != "" {
		t.Fatalf("空字符串进程环境变量应优先，期望空字符串，得到 %q", got)
	}
}

// TestLookupReportsExplicitEmptyValue 验证 Lookup 能区分显式空值和未设置。
func TestLookupReportsExplicitEmptyValue(t *testing.T) {
	e := NewEnv()
	e.data["DB_PASS"] = "from-file"

	t.Setenv("DB_PASS", "")

	got, ok := e.Lookup("DB_PASS")
	if !ok {
		t.Fatal("显式设置为空的环境变量应报告存在")
	}
	if got != "" {
		t.Fatalf("显式空环境变量应返回空字符串，实际为 %q", got)
	}
}

// TestGetFallsBackToFile 验证未设置 OS 环境变量时回退到 .env 文件值。
func TestGetFallsBackToFile(t *testing.T) {
	os.Unsetenv("THINKGO_ENV_TEST_KEY")

	e := NewEnv()
	e.data["THINKGO_ENV_TEST_KEY"] = "from-file"

	if got := e.Get("THINKGO_ENV_TEST_KEY"); got != "from-file" {
		t.Fatalf("应回退到 .env 文件值，期望 from-file，得到 %q", got)
	}
}

// TestGetReturnsDefault 验证 OS 环境变量与 .env 都缺失时返回默认值。
func TestGetReturnsDefault(t *testing.T) {
	os.Unsetenv("THINKGO_ENV_MISSING_KEY")

	e := NewEnv()
	if got := e.Get("THINKGO_ENV_MISSING_KEY", "fallback"); got != "fallback" {
		t.Fatalf("应返回默认值，期望 fallback，得到 %q", got)
	}
}
