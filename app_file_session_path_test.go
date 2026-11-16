package framework

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/cookie"
	sessionDriver "github.com/zhuhanxin0308/thinkgo/v3/session/driver"
)

func TestAppFileSessionRejectsEscapingLinksBeforeTouchingExternalDirectory(t *testing.T) {
	app := NewAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })
	runtimePath := app.ApplicationRuntimePath()
	inside := filepath.Join(runtimePath, "inside")
	if err := os.MkdirAll(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Chmod(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"escape": outside, "inside-link": inside} {
		if err := os.Symlink(target, filepath.Join(runtimePath, name)); err != nil {
			if runtime.GOOS == "windows" {
				t.Skipf("Windows 未启用目录链接权限: %v", err)
			}
			t.Fatal(err)
		}
	}
	factory, err := cookie.NewCookie(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, configured := range []string{"escape", "escape/new-session-dir"} {
		manager, err := createAppSession(app, map[string]interface{}{"type": "file", "storage_path": configured}, factory)
		if !errors.Is(err, sessionDriver.ErrInvalidSessionPath) || manager != nil {
			if manager != nil {
				_ = manager.Close()
			}
			t.Errorf("越出运行目录的相对路径应明确拒绝: %q, %v", configured, err)
		}
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("拒绝相对路径前已在外部创建目录: %v, %v", entries, err)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(outside); err != nil || info.Mode().Perm() != 0o755 {
			t.Fatalf("拒绝相对路径前已修改外部目录权限: %v, %v", info, err)
		}
	}
	manager, err := createAppSession(app, map[string]interface{}{"type": "file", "storage_path": "inside-link/records"}, factory)
	if err != nil {
		t.Fatalf("指向 runtime 内的链接应可用: %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(inside, "records")); err != nil || !info.IsDir() {
		t.Fatalf("未在内部链接目标创建文件会话目录: %v, %v", info, err)
	}
}

func TestAppFileSessionRetainsRelativeAndExplicitAbsoluteDirectories(t *testing.T) {
	app := NewAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })
	absolute := filepath.Join(t.TempDir(), "explicit")
	factory, err := cookie.NewCookie(nil)
	if err != nil {
		t.Fatal(err)
	}
	for configured, expected := range map[string]string{
		"runtime/sessions": filepath.Join(app.ApplicationRuntimePath(), "sessions"),
		absolute:           absolute,
	} {
		manager, err := createAppSession(app, map[string]interface{}{"type": "file", "storage_path": configured}, factory)
		if err != nil {
			t.Fatal(err)
		}
		if manager.GetConfig().StoragePath != expected {
			t.Errorf("有效路径解析错误: %q", manager.GetConfig().StoragePath)
		}
		if err := manager.Close(); err != nil {
			t.Fatal(err)
		}
		if info, err := os.Stat(expected); err != nil || !info.IsDir() {
			t.Fatalf("未创建指定存储目录: %v, %v", info, err)
		}
	}
}
