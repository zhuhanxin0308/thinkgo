package env

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// TestLoadMissingFileIsOptional 验证可选 .env 缺失时不会阻止应用启动。
func TestLoadMissingFileIsOptional(t *testing.T) {
	missing := filepath.Join(t.TempDir(), ".env")
	if err := NewEnv().Load(missing); err != nil {
		t.Fatalf("缺失的可选 .env 应被忽略，实际错误为 %v", err)
	}
}

// TestLoadRejectsMalformedLineWithoutPartialMutation 验证语法错误包含行号且不会留下部分配置。
func TestLoadRejectsMalformedLineWithoutPartialMutation(t *testing.T) {
	file := filepath.Join(t.TempDir(), ".env")
	content := "VALID_KEY=before\nMALFORMED_LINE\nAFTER_KEY=after\n"
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatalf("写入测试 .env 失败: %v", err)
	}

	e := NewEnv()
	err := e.Load(file)
	if err == nil {
		t.Fatal("畸形 .env 行必须返回错误")
	}
	if !strings.Contains(err.Error(), file) || !strings.Contains(err.Error(), "2") {
		t.Fatalf("语法错误必须包含文件和行号，实际为 %v", err)
	}
	if _, ok := e.Lookup("VALID_KEY"); ok {
		t.Fatal("加载失败时不得提交语法错误前的部分数据")
	}
}

// TestLoadRejectsInvalidKeyAndUnclosedQuote 验证非法键名和未闭合引号均会失败关闭。
func TestLoadRejectsInvalidKeyAndUnclosedQuote(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "invalid_key", content: "INVALID-KEY=value\n"},
		{name: "unclosed_quote", content: "TOKEN=\"secret\n"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), ".env")
			if err := os.WriteFile(file, []byte(test.content), 0o600); err != nil {
				t.Fatalf("写入测试 .env 失败: %v", err)
			}
			if err := NewEnv().Load(file); err == nil || !strings.Contains(err.Error(), "1") {
				t.Fatalf("非法 .env 内容必须返回带行号错误，实际为 %v", err)
			}
		})
	}
}

// TestGetBoolUsesStandardParsing 验证布尔环境变量支持标准大小写和值集合。
func TestGetBoolUsesStandardParsing(t *testing.T) {
	e := NewEnv()
	e.data["FEATURE_TRUE"] = " TRUE "
	e.data["FEATURE_FALSE"] = "0"

	gotTrue, err := e.GetBool("FEATURE_TRUE")
	if err != nil || !gotTrue {
		t.Fatalf("TRUE 应解析为 true，got=%v err=%v", gotTrue, err)
	}
	gotFalse, err := e.GetBool("FEATURE_FALSE", true)
	if err != nil || gotFalse {
		t.Fatalf("0 应解析为 false，got=%v err=%v", gotFalse, err)
	}
	missing, err := e.GetBool("FEATURE_MISSING", true)
	if err != nil || !missing {
		t.Fatalf("缺失键应返回默认值，got=%v err=%v", missing, err)
	}
}

// TestGetBoolRejectsInvalidValue 验证已设置的非法布尔值不会静默回退默认值。
func TestGetBoolRejectsInvalidValue(t *testing.T) {
	e := NewEnv()
	e.data["FEATURE_FLAG"] = "sometimes"

	got, err := e.GetBool("FEATURE_FLAG", true)
	if err == nil {
		t.Fatalf("非法布尔值必须返回错误，实际 got=%v", got)
	}
	if !strings.Contains(err.Error(), "FEATURE_FLAG") || !strings.Contains(err.Error(), "sometimes") {
		t.Fatalf("布尔解析错误必须包含键和值，实际为 %v", err)
	}
}

// TestLoadValidFileCommitsAllValues 验证合法文件会一次性提交空值、引号值和值中等号。
func TestLoadValidFileCommitsAllValues(t *testing.T) {
	file := filepath.Join(t.TempDir(), ".env")
	content := strings.Join([]string{
		"# comment",
		"",
		"PLAIN=value",
		"QUOTED=\"secret value\"",
		"SINGLE='single value'",
		"TOKEN=header.payload=signature",
		"EMPTY=",
	}, "\n")
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatalf("写入测试 .env 失败: %v", err)
	}

	e := NewEnv()
	if err := e.Load(file); err != nil {
		t.Fatalf("合法 .env 不应加载失败: %v", err)
	}
	wants := map[string]string{
		"PLAIN":  "value",
		"QUOTED": "secret value",
		"SINGLE": "single value",
		"TOKEN":  "header.payload=signature",
		"EMPTY":  "",
	}
	for key, want := range wants {
		got, ok := e.Lookup(key)
		if !ok || got != want {
			t.Fatalf("环境变量 %s 加载错误，want=%q got=%q ok=%v", key, want, got, ok)
		}
	}
}

// TestLoadRejectsDuplicateCaseInsensitiveKeys 验证不同大小写的重复环境键不会静默覆盖。
func TestLoadRejectsDuplicateCaseInsensitiveKeys(t *testing.T) {
	file := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(file, []byte("APP_MODE=first\napp_mode=second\n"), 0o600); err != nil {
		t.Fatalf("写入重复环境键失败: %v", err)
	}
	e := NewEnv()
	if err := e.Load(file); !errors.Is(err, ErrEnvDuplicateKey) {
		t.Fatalf("重复环境键应返回 ErrEnvDuplicateKey，实际为 %v", err)
	}
	if _, ok := e.Lookup("APP_MODE"); ok {
		t.Fatal("重复环境键加载失败后不应提交部分数据")
	}
}

// TestLoadRejectsOversizedFile 验证环境文件超过总字节限制时不会进入解析流程。
func TestLoadRejectsOversizedFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), ".env")
	content := strings.Repeat("A", int(maxEnvFileBytes)+1)
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatalf("写入超大环境文件失败: %v", err)
	}
	if err := NewEnv().Load(file); !errors.Is(err, ErrEnvFileTooLarge) {
		t.Fatalf("超大环境文件应返回 ErrEnvFileTooLarge，实际为 %v", err)
	}
}

// TestLoadRejectsSymlink 验证环境文件符号链接不会被跟随读取。
func TestLoadRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.env")
	link := filepath.Join(dir, ".env")
	if err := os.WriteFile(target, []byte("SECRET=value\n"), 0o600); err != nil {
		t.Fatalf("写入符号链接目标失败: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("当前 Windows 环境不允许创建符号链接: %v", err)
		}
		t.Fatalf("创建环境文件符号链接失败: %v", err)
	}
	if err := NewEnv().Load(link); !errors.Is(err, ErrEnvSymlinkNotAllowed) {
		t.Fatalf("环境文件符号链接应返回 ErrEnvSymlinkNotAllowed，实际为 %v", err)
	}
}

// TestGetBoolMissingWithoutDefault 验证未设置布尔变量且无默认值时返回 false。
func TestGetBoolMissingWithoutDefault(t *testing.T) {
	got, err := NewEnv().GetBool("THINKGO_UNSET_BOOL")
	if err != nil || got {
		t.Fatalf("缺失布尔变量应返回 false,nil，got=%v err=%v", got, err)
	}
}

// TestLoadDirectoryReturnsReadError 验证非普通文件产生的读取错误不会被当作缺失配置忽略。
func TestLoadDirectoryReturnsReadError(t *testing.T) {
	err := NewEnv().Load(t.TempDir())
	if err == nil {
		t.Fatal("把目录作为 .env 加载必须返回读取错误")
	}
}

// TestZeroValueEnvCanLoad 验证导出类型的零值也能安全加载配置。
func TestZeroValueEnvCanLoad(t *testing.T) {
	file := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(file, []byte("ZERO_VALUE=ready\n"), 0o600); err != nil {
		t.Fatalf("写入测试 .env 失败: %v", err)
	}

	var e Env
	if err := e.Load(file); err != nil {
		t.Fatalf("Env 零值加载失败: %v", err)
	}
	if got := e.Get("ZERO_VALUE"); got != "ready" {
		t.Fatalf("Env 零值未保存加载结果，实际为 %q", got)
	}
}
