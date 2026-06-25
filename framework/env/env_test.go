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

	os.Setenv("SERVER_PORT", "9999")
	defer os.Unsetenv("SERVER_PORT")

	if got := e.Get("SERVER_PORT"); got != "9999" {
		t.Fatalf("真实环境变量应优先，期望 9999，得到 %q", got)
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
