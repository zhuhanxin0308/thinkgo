package command

import (
	"os"
	"path/filepath"
	"testing"
)

// TestIsValidPort 校验端口校验逻辑：仅接受 1-65535 的整数。
func TestIsValidPort(t *testing.T) {
	cases := map[string]bool{
		"8080":  true,
		"1":     true,
		"65535": true,
		"0":     false,
		"65536": false,
		"70000": false,
		"-1":    false,
		"abc":   false,
		"":      false,
		"80a":   false,
	}
	for input, want := range cases {
		if got := isValidPort(input); got != want {
			t.Errorf("isValidPort(%q)=%v, 期望 %v", input, got, want)
		}
	}
}

// TestRunDeclaresPortOption 验证 run 命令声明了 --port/-p 选项，使帮助可展示且 Parse 可解析。
func TestRunDeclaresPortOption(t *testing.T) {
	cmd := &Run{}
	cmd.Configure()

	defs := cmd.GetOptionDefinitions()
	if len(defs) != 1 {
		t.Fatalf("run 应声明 1 个选项，得到 %d", len(defs))
	}
	if defs[0].Name != "port" || defs[0].Short != "p" {
		t.Fatalf("run 选项应为 port/-p，得到 %s/-%s", defs[0].Name, defs[0].Short)
	}
}

// TestRunEnsuresServerBinaryDirectory 验证热重载构建前会准备 bin 目录。
func TestRunEnsuresServerBinaryDirectory(t *testing.T) {
	basePath := t.TempDir()

	if err := ensureServerBinaryDir(basePath); err != nil {
		t.Fatalf("准备 server 二进制目录失败: %v", err)
	}
	if info, err := os.Stat(filepath.Join(basePath, "bin")); err != nil || !info.IsDir() {
		t.Fatalf("bin 目录应存在，info=%#v err=%v", info, err)
	}
}
