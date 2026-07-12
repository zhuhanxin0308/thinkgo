package command

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"thinkgo/framework"
	"thinkgo/framework/cache"
	"thinkgo/framework/console"
)

type clearFailingCacheDriver struct {
	err error
}

func (d *clearFailingCacheDriver) Get(string) (interface{}, bool, error)        { return nil, false, d.err }
func (d *clearFailingCacheDriver) Set(string, interface{}, time.Duration) error { return d.err }
func (d *clearFailingCacheDriver) Has(string) (bool, error)                     { return false, d.err }
func (d *clearFailingCacheDriver) Delete(string) error                          { return d.err }
func (d *clearFailingCacheDriver) Clear() error                                 { return d.err }
func (d *clearFailingCacheDriver) Inc(string, int64) (int64, error)             { return 0, d.err }
func (d *clearFailingCacheDriver) Dec(string, int64) (int64, error)             { return 0, d.err }

// TestClearOnlyRemovesCacheDirectory 验证 clear 只清理缓存目录，不误删 runtime 内的日志、会话和证书。
func TestClearOnlyRemovesCacheDirectory(t *testing.T) {
	basePath := t.TempDir()
	runtimePath := filepath.Join(basePath, "runtime")
	cachePath := filepath.Join(runtimePath, "cache")
	logPath := filepath.Join(runtimePath, "log")
	sessionPath := filepath.Join(runtimePath, "session")

	for _, dir := range []string{cachePath, logPath, sessionPath} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("创建测试目录失败: %v", err)
		}
	}

	files := map[string]string{
		filepath.Join(cachePath, "old.cache"):       "cache",
		filepath.Join(cachePath, "active.lock"):     "lock",
		filepath.Join(cachePath, "keep.txt"):        "unmanaged",
		filepath.Join(logPath, "app.log"):           "log",
		filepath.Join(sessionPath, "session.data"):  "session",
		filepath.Join(runtimePath, "cert.pem"):      "cert",
		filepath.Join(runtimePath, "ip2region.xdb"): "ipdb",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("写入测试文件失败: %v", err)
		}
	}

	cmd := &Clear{Command: console.Command{App: &framework.App{BasePath: basePath}}}
	if err := cmd.Execute(console.NewInput(), console.NewOutput()); err != nil {
		t.Fatalf("清理缓存失败: %v", err)
	}

	if _, err := os.Stat(filepath.Join(cachePath, "old.cache")); !os.IsNotExist(err) {
		t.Fatalf("clear 应删除旧缓存文件，实际错误: %v", err)
	}
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("clear 后缓存目录应被重建: %v", err)
	}
	for _, path := range []string{
		filepath.Join(cachePath, "active.lock"),
		filepath.Join(cachePath, "keep.txt"),
		filepath.Join(logPath, "app.log"),
		filepath.Join(sessionPath, "session.data"),
		filepath.Join(runtimePath, "cert.pem"),
		filepath.Join(runtimePath, "ip2region.xdb"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("clear 不应删除非缓存运行时文件 %s: %v", path, err)
		}
	}
}

// TestClearReportsCacheBackendFailure 验证后端清理失败时命令不会继续删除本地目录或谎报成功。
func TestClearReportsCacheBackendFailure(t *testing.T) {
	basePath := t.TempDir()
	cachePath := filepath.Join(basePath, "runtime", "cache")
	if err := os.MkdirAll(cachePath, 0o700); err != nil {
		t.Fatalf("创建缓存目录失败: %v", err)
	}
	markerPath := filepath.Join(cachePath, "old.cache")
	if err := os.WriteFile(markerPath, []byte("cache"), 0o600); err != nil {
		t.Fatalf("写入缓存标记失败: %v", err)
	}
	backendErr := errors.New("backend unavailable")
	app := &framework.App{
		BasePath: basePath,
		Cache:    cache.NewCache(nil, &clearFailingCacheDriver{err: backendErr}),
	}
	cmd := &Clear{Command: console.Command{App: app}}
	if err := cmd.Execute(console.NewInput(), console.NewOutput()); !errors.Is(err, backendErr) {
		t.Fatalf("命令应返回缓存后端错误，实际为 %v", err)
	}
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("后端失败后不应继续删除本地缓存目录: %v", err)
	}
}

// TestClearRejectsMissingDependenciesAndInvalidLocalPath 验证清理命令不会在
// 应用上下文缺失或缓存路径被普通文件占用时假装成功。
func TestClearRejectsMissingDependenciesAndInvalidLocalPath(t *testing.T) {
	output := console.NewOutput()
	if err := (&Clear{}).Execute(console.NewInput(), nil); !errors.Is(err, console.ErrInvalidOutput) {
		t.Fatalf("空输出应返回 ErrInvalidOutput，实际为 %v", err)
	}
	if err := (&Clear{}).Execute(console.NewInput(), output); !errors.Is(err, framework.ErrNilApplication) {
		t.Fatalf("空应用应返回 ErrNilApplication，实际为 %v", err)
	}
	if err := (&Clear{Command: console.Command{App: &framework.App{}}}).Execute(console.NewInput(), output); err == nil {
		t.Fatal("空应用根目录应返回错误")
	}

	basePath := t.TempDir()
	runtimePath := filepath.Join(basePath, "runtime")
	if err := os.Mkdir(runtimePath, 0o700); err != nil {
		t.Fatalf("创建 runtime 目录失败: %v", err)
	}
	cachePath := filepath.Join(runtimePath, "cache")
	if err := os.WriteFile(cachePath, []byte("path-conflict"), 0o600); err != nil {
		t.Fatalf("创建缓存路径冲突文件失败: %v", err)
	}
	command := &Clear{Command: console.Command{App: &framework.App{BasePath: basePath}}}
	if err := command.Execute(console.NewInput(), output); err == nil {
		t.Fatal("缓存目录被文件占用时应返回错误")
	}
	content, err := os.ReadFile(cachePath)
	if err != nil || string(content) != "path-conflict" {
		t.Fatalf("失败后不应破坏冲突文件: content=%q err=%v", content, err)
	}
}
