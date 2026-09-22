package framework

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/filesystem"
)

// TestAppFilesystemMatchesThinkPHPDefaults 验证应用无需业务装配代码即可使用
// runtime/storage 默认磁盘和 public/storage 公共磁盘。
func TestAppFilesystemMatchesThinkPHPDefaults(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	app, err := BuildApp(basePath)
	if err != nil {
		t.Fatalf("构建测试应用失败: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })

	manager := app.Filesystem()
	if manager == nil || manager.GetDefaultDriver() != "local" {
		t.Fatalf("文件系统门面未安装默认磁盘: %#v", manager)
	}
	resolved, err := ResolveServiceAs[*filesystem.Filesystem](app, ServiceFilesystem)
	if err != nil || resolved != manager {
		t.Fatalf("容器文件系统与 App 门面不是同一实例: resolved=%p facade=%p err=%v", resolved, manager, err)
	}
	storagePath := filepath.Join(basePath, "runtime", "storage")
	if _, statErr := os.Stat(storagePath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("未调用 Disk 前不应提前创建磁盘根目录: %v", statErr)
	}
	localDisk, err := manager.Disk()
	if err != nil {
		t.Fatalf("解析默认磁盘失败: %v", err)
	}
	if err = localDisk.Write("reports/ready.txt", "ready"); err != nil {
		t.Fatalf("默认磁盘写入失败: %v", err)
	}
	if content, readErr := localDisk.Read("reports/ready.txt"); readErr != nil || content != "ready" {
		t.Fatalf("默认磁盘读取错误: content=%q err=%v", content, readErr)
	}
	publicDisk, err := manager.Disk("public")
	if err != nil {
		t.Fatalf("解析公共磁盘失败: %v", err)
	}
	if url, urlErr := publicDisk.URL("images/logo.png"); urlErr != nil || url != "/storage/images/logo.png" {
		t.Fatalf("公共磁盘 URL 错误: url=%q err=%v", url, urlErr)
	}
}
