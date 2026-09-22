package command

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/framework"
	"github.com/zhuhanxin0308/thinkgo/framework/console"
)

// TestClearDefaultsToCacheAndPreservesRuntime 验证无选项时保留非缓存运行文件。
func TestClearDefaultsToCacheAndPreservesRuntime(t *testing.T) {
	basePath := t.TempDir()
	for relative, content := range map[string]string{
		"runtime/cache/value.cache": "cache",
		"runtime/log/app.log":       "log",
		"runtime/session/data":      "session",
		"runtime/.gitignore":        "*",
	} {
		writeClearTestFile(t, basePath, relative, content)
	}
	application := buildConsoleTestApp(t, basePath)
	command := &Clear{Command: console.Command{App: application}}
	if err := command.Execute(console.NewInput(), console.NewOutput()); err != nil {
		t.Fatalf("清理 runtime 失败: %v", err)
	}
	for _, relative := range []string{"runtime/cache/value.cache", "runtime/log/app.log", "runtime/session/data", "runtime/.gitignore"} {
		if _, err := os.Stat(filepath.Join(basePath, filepath.FromSlash(relative))); err != nil {
			t.Fatalf("默认清理应保留 %s: %v", relative, err)
		}
	}
}

// TestClearSelectsNativeApplicationRuntime 验证 clear [app] 使用已注册应用的独立缓存服务。
func TestClearSelectsNativeApplicationRuntime(t *testing.T) {
	project := newCommandMultiApplicationProject(t)
	applications, err := compiledApplicationsForCommand(project, "", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, current := range applications {
		if err := current.application.Cache().Set("value", current.name); err != nil {
			t.Fatal(err)
		}
	}
	command := &Clear{Command: console.Command{App: project}}
	if err := command.Execute(console.NewInput("admin", "--cache"), console.NewOutput()); err != nil {
		t.Fatal(err)
	}
	for _, current := range applications {
		_, found, err := current.application.Cache().Get("value")
		if err != nil || found != (current.name == "index") {
			t.Errorf("应用缓存隔离错误: %s %t %v", current.name, found, err)
		}
	}
	if err := command.Execute(console.NewInput("missing"), console.NewOutput()); err == nil {
		t.Fatal("未知应用必须被拒绝")
	}
}

// TestClearPathSelectorsAndDirectoryRemoval 验证 cache、log、path 和 dir
// 选项遵循 ThinkPHP 的选择优先级与目录清理语义。
func TestClearPathSelectorsAndDirectoryRemoval(t *testing.T) {
	testCases := []struct {
		name      string
		args      []string
		removed   []string
		preserved []string
	}{
		{name: "cache", args: []string{"--cache"}, preserved: []string{"runtime/cache/value", "runtime/log/app.log"}},
		{name: "log", args: []string{"--log"}, removed: []string{"runtime/log/app.log"}, preserved: []string{"runtime/cache/value"}},
		{name: "path", args: []string{"--path", "custom"}, removed: []string{"custom/value"}, preserved: []string{"runtime/cache/value", "runtime/log/app.log"}},
		{name: "dir", args: []string{"--dir"}, removed: []string{"runtime/cache/empty"}, preserved: []string{"runtime/cache/value", "runtime/log/app.log"}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			basePath := t.TempDir()
			for _, relative := range []string{"runtime/cache/value", "runtime/log/app.log", "custom/value"} {
				writeClearTestFile(t, basePath, relative, relative)
			}
			application := buildConsoleTestApp(t, basePath)
			if err := os.MkdirAll(filepath.Join(basePath, "runtime/cache/empty/nested"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := application.Cache().Set("managed", "cached"); err != nil {
				t.Fatal(err)
			}
			command := &Clear{Command: console.Command{App: application}}
			if err := command.Execute(console.NewInput(testCase.args...), console.NewOutput()); err != nil {
				t.Fatalf("执行 clear 失败: %v", err)
			}
			_, found, err := application.Cache().Get("managed")
			if err != nil || found != (testCase.name == "log" || testCase.name == "path") {
				t.Fatalf("缓存选择错误: %t %v", found, err)
			}
			for _, relative := range testCase.removed {
				if _, err := os.Stat(filepath.Join(basePath, filepath.FromSlash(relative))); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("目标未清理 %s: %v", relative, err)
				}
			}
			for _, relative := range testCase.preserved {
				if _, err := os.Stat(filepath.Join(basePath, filepath.FromSlash(relative))); err != nil {
					t.Errorf("非目标被误清理 %s: %v", relative, err)
				}
			}
		})
	}
}

// TestClearExpireOnlyRemovesExpiredCacheFiles 验证 expire 仅在 cache 模式下
// 删除已过期的文件缓存，不破坏未过期或无法识别的内容。
func TestClearExpireOnlyRemovesExpiredCacheFiles(t *testing.T) {
	basePath := t.TempDir()
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	pastName := fmt.Sprintf("%x.cache", sha256.Sum256([]byte("past")))
	futureName := fmt.Sprintf("%x.cache", sha256.Sum256([]byte("future")))
	writeClearTestFile(t, basePath, "runtime/cache/"+pastName, `{"key":"past","value":true,"expiry":"`+past+`"}`)
	writeClearTestFile(t, basePath, "runtime/cache/"+futureName, `{"key":"future","value":true,"expiry":"`+future+`"}`)
	writeClearTestFile(t, basePath, "runtime/cache/invalid.cache", "invalid")
	application := buildConsoleTestApp(t, basePath)
	command := &Clear{Command: console.Command{App: application}}
	if err := command.Execute(console.NewInput("--cache", "--expire"), console.NewOutput()); err != nil {
		t.Fatalf("清理过期缓存失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(basePath, "runtime", "cache", pastName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("过期缓存未删除: %v", err)
	}
	for _, name := range []string{futureName, "invalid.cache"} {
		if _, err := os.Stat(filepath.Join(basePath, "runtime", "cache", name)); err != nil {
			t.Fatalf("未过期或无效缓存不应删除 %s: %v", name, err)
		}
	}
}

// TestClearRejectsMissingDependenciesAndUnsafeTargets 验证清理命令不会在
// 缺少应用上下文、目标为普通文件或目标等于项目根目录时执行删除。
func TestClearRejectsMissingDependenciesAndUnsafeTargets(t *testing.T) {
	output := console.NewOutput()
	if err := (&Clear{}).Execute(console.NewInput(), nil); !errors.Is(err, console.ErrInvalidOutput) {
		t.Fatalf("空输出应返回 ErrInvalidOutput，实际为 %v", err)
	}
	if err := (&Clear{}).Execute(console.NewInput(), output); !errors.Is(err, framework.ErrNilApplication) {
		t.Fatalf("空应用应返回 ErrNilApplication，实际为 %v", err)
	}
	basePath := t.TempDir()
	writeClearTestFile(t, basePath, "not-a-directory", "value")
	application := buildConsoleTestApp(t, basePath)
	command := &Clear{Command: console.Command{App: application}}
	if err := command.Execute(console.NewInput("--path", "not-a-directory"), output); err == nil {
		t.Fatal("普通文件目标应返回错误")
	}
	if err := command.Execute(console.NewInput("--path", basePath), output); err == nil {
		t.Fatal("项目根目录不得作为 clear 目标")
	}
}

func writeClearTestFile(t *testing.T, basePath, relative, content string) {
	t.Helper()
	path := filepath.Join(basePath, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建 clear 测试目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写入 clear 测试文件失败: %v", err)
	}
}
