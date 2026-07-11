package command

import (
	"os"
	"path/filepath"
	"testing"

	"thinkgo/framework"
	"thinkgo/framework/console"
)

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
	cmd.Execute(console.NewInput(), console.NewOutput())

	if _, err := os.Stat(filepath.Join(cachePath, "old.cache")); !os.IsNotExist(err) {
		t.Fatalf("clear 应删除旧缓存文件，实际错误: %v", err)
	}
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("clear 后缓存目录应被重建: %v", err)
	}
	for _, path := range []string{
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
