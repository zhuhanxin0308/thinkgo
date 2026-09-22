package filesystem

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// TestFilesystemDiskAndConfigMatchThinkPHP 验证 disk、getConfig、
// getDiskConfig 和 getDefaultDriver 的默认调用方式。
func TestFilesystemDiskAndConfigMatchThinkPHP(t *testing.T) {
	basePath := t.TempDir()
	manager := New(map[string]interface{}{
		"default": "local",
		"disks": map[string]interface{}{
			"local": map[string]interface{}{
				"root": filepath.Join(basePath, "runtime", "storage"),
			},
			"public": map[string]interface{}{
				"type":       "local",
				"root":       filepath.Join(basePath, "public", "storage"),
				"url":        "/storage",
				"visibility": "public",
			},
		},
	})
	t.Cleanup(func() { _ = manager.Close() })

	if manager.GetDefaultDriver() != "local" {
		t.Fatalf("默认磁盘错误: %q", manager.GetDefaultDriver())
	}
	defaultDisk, err := manager.Disk()
	if err != nil {
		t.Fatalf("解析默认磁盘失败: %v", err)
	}
	localDisk, err := manager.Disk("local")
	if err != nil || defaultDisk != localDisk {
		t.Fatalf("默认磁盘必须复用 local 实例: default=%p local=%p err=%v", defaultDisk, localDisk, err)
	}
	publicDisk, err := manager.Disk("public")
	if err != nil {
		t.Fatalf("解析 public 磁盘失败: %v", err)
	}
	if got, err := publicDisk.URL("avatar/user.png"); err != nil || got != "/storage/avatar/user.png" {
		t.Fatalf("public URL 错误: url=%q err=%v", got, err)
	}
	if got, err := manager.GetDiskConfig("public", "visibility"); err != nil || got != "public" {
		t.Fatalf("磁盘配置读取错误: value=%#v err=%v", got, err)
	}
	if got, err := manager.GetDiskConfig("public", "missing", "fallback"); err != nil || got != "fallback" {
		t.Fatalf("磁盘配置默认值错误: value=%#v err=%v", got, err)
	}
	if got := manager.GetConfig("default"); got != "local" {
		t.Fatalf("顶层配置读取错误: %#v", got)
	}
	if got := manager.GetConfig("missing", "fallback"); got != "fallback" {
		t.Fatalf("顶层配置默认值错误: %#v", got)
	}
	if got, ok := manager.GetConfig().(map[string]interface{}); !ok || got["default"] != "local" {
		t.Fatalf("完整配置读取错误: %#v", manager.GetConfig())
	}
	if _, err := manager.Disk("missing"); !errors.Is(err, ErrDiskNotFound) {
		t.Fatal("不存在的磁盘必须返回错误")
	}
}

// TestFilesystemResolvesDriversLazily 验证构造管理器时不会提前解析磁盘，
// 实际调用 disk 后才报告默认磁盘或驱动类型错误。
func TestFilesystemResolvesDriversLazily(t *testing.T) {
	missingDefault := New(map[string]interface{}{"default": "missing", "disks": map[string]interface{}{}})
	if _, err := missingDefault.Disk(); !errors.Is(err, ErrDiskNotFound) {
		t.Fatalf("缺失默认磁盘应在首次解析时失败: %v", err)
	}

	unsupported := New(map[string]interface{}{
		"default": "cloud",
		"disks": map[string]interface{}{
			"cloud": map[string]interface{}{"type": "s3", "custom": true},
		},
	})
	if _, err := unsupported.Disk(); !errors.Is(err, ErrDriverNotSupported) {
		t.Fatalf("不支持的驱动应在首次解析时失败: %v", err)
	}
}

// TestFilesystemConcurrentCloseWaitsForSameResult 验证并发关闭调用都会等待
// 已实例化磁盘关闭完成，并观察到完全相同的聚合错误。
func TestFilesystemConcurrentCloseWaitsForSameResult(t *testing.T) {
	closeFailure := errors.New("disk close failed")
	driver := &blockingCloseDriver{started: make(chan struct{}), release: make(chan struct{}), err: closeFailure}
	manager := New(map[string]interface{}{
		"default": "custom",
		"disks": map[string]interface{}{
			"custom": map[string]interface{}{"type": "blocking"},
		},
	})
	if err := manager.Extend("blocking", func(map[string]interface{}) (Driver, error) { return driver, nil }); err != nil {
		t.Fatalf("注册测试驱动失败: %v", err)
	}
	if _, err := manager.Disk(); err != nil {
		t.Fatalf("实例化测试磁盘失败: %v", err)
	}

	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() { first <- manager.Close() }()
	<-driver.started
	go func() { second <- manager.Close() }()
	select {
	case err := <-second:
		t.Fatalf("第二个 Close 不应在磁盘关闭完成前返回: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(driver.release)
	if err := <-first; !errors.Is(err, closeFailure) {
		t.Fatalf("首次 Close 错误错误: %v", err)
	}
	if err := <-second; !errors.Is(err, closeFailure) {
		t.Fatalf("并发 Close 未返回相同错误: %v", err)
	}
}

type blockingCloseDriver struct {
	Driver
	started chan struct{}
	release chan struct{}
	err     error
}

func (driver *blockingCloseDriver) Close() error {
	close(driver.started)
	<-driver.release
	return driver.err
}
