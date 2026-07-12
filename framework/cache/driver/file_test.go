package driver

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestFileDriver(t *testing.T) (*File, string) {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "cache")
	driver, err := NewFile(directory)
	if err != nil {
		t.Fatalf("创建文件缓存驱动失败: %v", err)
	}
	return driver, directory
}

// TestFileCacheRoundTripWithUnsafeKey 验证任意业务键只映射到哈希文件，并支持 nil 命中和重复覆盖。
func TestFileCacheRoundTripWithUnsafeKey(t *testing.T) {
	basePath := t.TempDir()
	cacheDir := filepath.Join(basePath, "cache")
	driver, err := NewFile(cacheDir)
	if err != nil {
		t.Fatalf("创建文件缓存驱动失败: %v", err)
	}
	protectedPath := filepath.Join(basePath, "protected.txt")
	if err = os.WriteFile(protectedPath, []byte("origin"), 0o600); err != nil {
		t.Fatalf("写入受保护文件失败: %v", err)
	}

	unsafeKey := "../protected.txt"
	for _, value := range []interface{}{"first", "cached-value", nil} {
		if err = driver.Set(unsafeKey, value, time.Minute); err != nil {
			t.Fatalf("覆盖写入文件缓存失败: %v", err)
		}
		actual, found, getErr := driver.Get(unsafeKey)
		if getErr != nil || !found || actual != value {
			t.Fatalf("文件缓存读写错误: value=%#v found=%t err=%v", actual, found, getErr)
		}
	}
	content, err := os.ReadFile(protectedPath)
	if err != nil || string(content) != "origin" {
		t.Fatalf("危险缓存键改写了目录外文件: content=%q err=%v", string(content), err)
	}
}

// TestFileCacheClearKeepsLocksAndUnmanagedFiles 验证 Flush 不会释放业务锁或删除非缓存文件。
func TestFileCacheClearKeepsLocksAndUnmanagedFiles(t *testing.T) {
	driver, cacheDir := newTestFileDriver(t)
	unmanagedPath := filepath.Join(cacheDir, "keep.txt")
	if err := os.WriteFile(unmanagedPath, []byte("origin"), 0o600); err != nil {
		t.Fatalf("写入非缓存文件失败: %v", err)
	}
	if err := driver.Set("managed", "cached-value", time.Minute); err != nil {
		t.Fatalf("写入缓存失败: %v", err)
	}
	if acquired, err := driver.AcquireLock("job", "owner-a", time.Minute); err != nil || !acquired {
		t.Fatalf("获取文件锁失败: acquired=%t err=%v", acquired, err)
	}
	if err := driver.Clear(); err != nil {
		t.Fatalf("清空文件缓存失败: %v", err)
	}
	if _, found, err := driver.Get("managed"); err != nil || found {
		t.Fatalf("Clear 后缓存项仍存在: found=%t err=%v", found, err)
	}
	if content, err := os.ReadFile(unmanagedPath); err != nil || string(content) != "origin" {
		t.Fatalf("Clear 删除了非缓存文件: content=%q err=%v", string(content), err)
	}
	if acquired, err := driver.AcquireLock("job", "owner-b", time.Minute); err != nil || acquired {
		t.Fatalf("Clear 不得释放现有锁: acquired=%t err=%v", acquired, err)
	}
	if released, err := driver.ReleaseLock("job", "owner-a"); err != nil || !released {
		t.Fatalf("原锁拥有者释放失败: released=%t err=%v", released, err)
	}
}

// TestFileCacheCounterPreservesExpiryAndRejectsInvalidValues 验证文件计数严格整数化、检测溢出并保留原 TTL。
func TestFileCacheCounterPreservesExpiryAndRejectsInvalidValues(t *testing.T) {
	driver, _ := newTestFileDriver(t)
	if err := driver.Set("counter", int64(5), 80*time.Millisecond); err != nil {
		t.Fatalf("写入计数缓存失败: %v", err)
	}
	if value, err := driver.Inc("counter", 2); err != nil || value != 7 {
		t.Fatalf("文件计数递增错误: value=%d err=%v", value, err)
	}
	counterPath := driver.cacheFilePath("counter")
	time.Sleep(120 * time.Millisecond)
	if _, found, err := driver.Get("counter"); err != nil || found {
		t.Fatalf("计数递增不应清除原 TTL: found=%t err=%v", found, err)
	}
	if _, err := os.Stat(counterPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("读取过期项后应物理回收缓存文件，实际为 %v", err)
	}

	if err := driver.Set("fraction", 1.5, 0); err != nil {
		t.Fatalf("写入小数测试值失败: %v", err)
	}
	if _, err := driver.Inc("fraction", 1); !errors.Is(err, ErrInvalidCounterValue) {
		t.Fatalf("小数计数值应被拒绝，实际为 %v", err)
	}
	if err := driver.Set("overflow", int64(math.MaxInt64), 0); err != nil {
		t.Fatalf("写入溢出测试值失败: %v", err)
	}
	if _, err := driver.Inc("overflow", 1); !errors.Is(err, ErrCounterOverflow) {
		t.Fatalf("计数溢出应返回 ErrCounterOverflow，实际为 %v", err)
	}
	if _, err := driver.Inc("negative-step", -1); !errors.Is(err, ErrInvalidCounterStep) {
		t.Fatalf("负递增步长应返回 ErrInvalidCounterStep，实际为 %v", err)
	}
	if _, err := driver.Dec("negative-step", -1); !errors.Is(err, ErrInvalidCounterStep) {
		t.Fatalf("负递减步长应返回 ErrInvalidCounterStep，实际为 %v", err)
	}
}

// TestFileCacheRejectsInvalidRootAndSymlinkEntry 验证缓存根目录和缓存项都不能被符号链接绕过。
func TestFileCacheRejectsInvalidRootAndSymlinkEntry(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "not-directory")
	if err := os.WriteFile(filePath, []byte("file"), 0o600); err != nil {
		t.Fatalf("写入普通文件失败: %v", err)
	}
	if _, err := NewFile(filePath); !errors.Is(err, ErrInvalidCachePath) {
		t.Fatalf("普通文件不能作为缓存根目录，实际为 %v", err)
	}
	if _, err := NewFile(filepath.Join(root, "bad\tpath")); !errors.Is(err, ErrInvalidCachePath) {
		t.Fatalf("包含控制字符的缓存目录应返回 ErrInvalidCachePath，实际为 %v", err)
	}

	driver, cacheDir := newTestFileDriver(t)
	outside := filepath.Join(root, "outside.cache")
	if err := os.WriteFile(outside, []byte(`{"val":"secret","expiry":"0001-01-01T00:00:00Z"}`), 0o600); err != nil {
		t.Fatalf("写入目录外文件失败: %v", err)
	}
	entryPath := driver.cacheFilePath("linked")
	if err := os.Symlink(outside, entryPath); err != nil {
		t.Skipf("当前环境不允许创建符号链接: %v", err)
	}
	if _, _, err := driver.Get("linked"); !errors.Is(err, ErrUnsafeCacheEntry) {
		t.Fatalf("符号链接缓存项应返回 ErrUnsafeCacheEntry，实际为 %v", err)
	}
	if relative, err := filepath.Rel(cacheDir, entryPath); err != nil || relative == ".." {
		t.Fatalf("测试缓存项未位于缓存目录内: relative=%q err=%v", relative, err)
	}
}

// TestFileCacheCRUDCorruptionAndSizeLimits 验证文件存在判断、幂等删除及异常文件的安全边界。
func TestFileCacheCRUDCorruptionAndSizeLimits(t *testing.T) {
	driver, _ := newTestFileDriver(t)
	if err := driver.Set("key", "value", 0); err != nil {
		t.Fatalf("写入文件缓存失败: %v", err)
	}
	if exists, err := driver.Has("key"); err != nil || !exists {
		t.Fatalf("Has 未识别文件缓存: exists=%t err=%v", exists, err)
	}
	if err := driver.Delete("key"); err != nil {
		t.Fatalf("删除文件缓存失败: %v", err)
	}
	if err := driver.Delete("key"); err != nil {
		t.Fatalf("重复删除文件缓存应幂等: %v", err)
	}
	if exists, err := driver.Has("key"); err != nil || exists {
		t.Fatalf("删除后 Has 仍命中: exists=%t err=%v", exists, err)
	}
	if value, err := driver.Dec("missing", 2); err != nil || value != -2 {
		t.Fatalf("缺失文件计数递减应从 0 开始: value=%d err=%v", value, err)
	}
	if err := driver.Set("negative-ttl", "value", -time.Second); !errors.Is(err, ErrInvalidDriverTTL) {
		t.Fatalf("负 TTL 应返回 ErrInvalidDriverTTL，实际为 %v", err)
	}

	corruptPath := driver.cacheFilePath("corrupt")
	if err := os.WriteFile(corruptPath, []byte("{"), 0o600); err != nil {
		t.Fatalf("写入损坏缓存文件失败: %v", err)
	}
	if _, _, err := driver.Get("corrupt"); !errors.Is(err, ErrCorruptCacheEntry) {
		t.Fatalf("损坏缓存文件应返回 ErrCorruptCacheEntry，实际为 %v", err)
	}
	unsafePath := driver.cacheFilePath("directory")
	if err := os.Mkdir(unsafePath, 0o700); err != nil {
		t.Fatalf("创建伪装缓存目录失败: %v", err)
	}
	if _, _, err := driver.Get("directory"); !errors.Is(err, ErrUnsafeCacheEntry) {
		t.Fatalf("非普通缓存项应返回 ErrUnsafeCacheEntry，实际为 %v", err)
	}
	if err := driver.Delete("directory"); !errors.Is(err, ErrUnsafeCacheEntry) {
		t.Fatalf("Delete 不得删除伪装缓存目录，实际为 %v", err)
	}
	if info, err := os.Stat(unsafePath); err != nil || !info.IsDir() {
		t.Fatalf("拒绝 Delete 后伪装目录状态错误: info=%#v err=%v", info, err)
	}
	if err := driver.Set("unsupported", make(chan int), 0); err == nil {
		t.Fatal("不可 JSON 序列化的值不得写入文件缓存")
	}
	oversizedPath := driver.cacheFilePath("oversized")
	handle, err := os.OpenFile(oversizedPath, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("创建超大缓存文件失败: %v", err)
	}
	if err = handle.Truncate(maxFileCacheEntryBytes + 1); err != nil {
		_ = handle.Close()
		t.Fatalf("扩展超大缓存文件失败: %v", err)
	}
	if err = handle.Close(); err != nil {
		t.Fatalf("关闭超大缓存文件失败: %v", err)
	}
	if _, _, err = driver.Get("oversized"); !errors.Is(err, ErrCacheEntryTooLarge) {
		t.Fatalf("超大缓存文件应返回 ErrCacheEntryTooLarge，实际为 %v", err)
	}
}

// TestFileCacheLockOwnershipExpiryAndCorruptRecovery 验证文件锁 owner、过期接管和陈旧损坏锁恢复。
func TestFileCacheLockOwnershipExpiryAndCorruptRecovery(t *testing.T) {
	driver, _ := newTestFileDriver(t)
	if acquired, err := driver.AcquireLock("short", "owner-a", 10*time.Millisecond); err != nil || !acquired {
		t.Fatalf("获取短文件锁失败: acquired=%t err=%v", acquired, err)
	}
	if released, err := driver.ReleaseLock("short", "owner-b"); err != nil || released {
		t.Fatalf("错误 owner 不得释放文件锁: released=%t err=%v", released, err)
	}
	time.Sleep(20 * time.Millisecond)
	if acquired, err := driver.AcquireLock("short", "owner-b", time.Second); err != nil || !acquired {
		t.Fatalf("过期后新 owner 应接管文件锁: acquired=%t err=%v", acquired, err)
	}
	if released, err := driver.ReleaseLock("short", "owner-b"); err != nil || !released {
		t.Fatalf("新 owner 释放文件锁失败: released=%t err=%v", released, err)
	}

	corruptPath := driver.lockFilePath("corrupt")
	if err := os.WriteFile(corruptPath, []byte("{"), 0o600); err != nil {
		t.Fatalf("写入损坏锁文件失败: %v", err)
	}
	oldTime := time.Now().Add(-2 * corruptLockRecoveryAge)
	if err := os.Chtimes(corruptPath, oldTime, oldTime); err != nil {
		t.Fatalf("设置损坏锁文件时间失败: %v", err)
	}
	if acquired, err := driver.AcquireLock("corrupt", "owner-c", time.Second); err != nil || !acquired {
		t.Fatalf("陈旧损坏锁应被安全恢复: acquired=%t err=%v", acquired, err)
	}
}
