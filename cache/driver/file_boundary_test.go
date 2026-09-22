package driver

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestFilePrefixCleanupReportsCorruption 验证损坏或归属错误的项不会被误删，正常匹配项仍能完成清理。
func TestFilePrefixCleanupReportsCorruption(t *testing.T) {
	driver, _ := newTestFileDriver(t)
	for _, key := range []string{"wanted:valid", "other:valid"} {
		if err := driver.Set(key, "value", 0); err != nil {
			t.Fatal(err)
		}
	}
	for key, data := range map[string]string{
		"wanted:corrupt":  "{",
		"wanted:mismatch": `{"key":"another:key","value":1}`,
		"wanted:legacy":   `{"value":1}`,
	} {
		if err := os.WriteFile(driver.cacheFilePath(key), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	err := driver.ClearPrefix("wanted:")
	if !errors.Is(err, ErrCorruptCacheEntry) || !errors.Is(err, ErrUnscopedCacheEntry) {
		t.Fatalf("清理吞掉了损坏或未知归属错误: %v", err)
	}
	if _, found, err := driver.Get("wanted:valid"); err != nil || found {
		t.Fatalf("其他条目错误阻断了合法清理: %t %v", found, err)
	}
	for _, key := range []string{"other:valid", "wanted:corrupt", "wanted:mismatch", "wanted:legacy"} {
		if _, err := os.Stat(driver.cacheFilePath(key)); err != nil {
			t.Fatalf("不应删除的文件被清理: %s %v", key, err)
		}
	}
	if err := driver.ClearPrefix(""); !errors.Is(err, ErrInvalidCachePrefix) {
		t.Fatalf("空清理前缀未被拒绝: %v", err)
	}
}

// TestFileWriteFailureLeavesNoTemporaryFile 验证目标被目录占用时写入失败并回收临时文件。
func TestFileWriteFailureLeavesNoTemporaryFile(t *testing.T) {
	driver, directory := newTestFileDriver(t)
	if err := os.Mkdir(driver.cacheFilePath("blocked"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := driver.Set("blocked", "value", 0); err == nil {
		t.Fatal("目标目录不能被当成缓存文件覆盖")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tmp-cache-") {
			t.Fatalf("失败写入遗留临时文件: %s", entry.Name())
		}
	}
}

// TestLeasePayloadRejectsBrokenHandlesAndOversizedOwners 验证共享文件原语的调用方保留载荷预算和句柄错误。
func TestLeasePayloadRejectsBrokenHandlesAndOversizedOwners(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lease.lock")
	handle, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	if err := writeCacheLockPayload(handle, fileLockPayload{Owner: strings.Repeat("x", maxFileLockBytes), Expiry: time.Now().Add(time.Minute)}); !errors.Is(err, ErrCacheEntryTooLarge) {
		t.Fatalf("超大 owner 未被拒绝: %v", err)
	}
	if err := handle.Truncate(maxFileLockBytes + 1); err != nil {
		t.Fatal(err)
	}
	if _, err := readCacheLockPayload(handle); !errors.Is(err, ErrCacheEntryTooLarge) {
		t.Fatalf("超大磁盘载荷未被拒绝: %v", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readCacheLockPayload(handle); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("关闭句柄的读取错误丢失: %v", err)
	}
	if err := writeCacheLockPayload(handle, fileLockPayload{Owner: "owner", Expiry: time.Now().Add(time.Minute)}); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("关闭句柄的写入错误丢失: %v", err)
	}
	if err := writeCacheLockPayload(nil, fileLockPayload{}); !errors.Is(err, ErrInvalidCacheLock) {
		t.Fatalf("空租约句柄未被拒绝: %v", err)
	}
	if _, err := readCacheLockPayload(nil); !errors.Is(err, ErrInvalidCacheLock) {
		t.Fatalf("空租约句柄读取未被拒绝: %v", err)
	}
}

// TestFileLeaseRejectsUnsafePersistentEntries 验证不可信磁盘租约在获取、续期和释放入口一致拒绝。
func TestFileLeaseRejectsUnsafePersistentEntries(t *testing.T) {
	driver, _ := newTestFileDriver(t)
	if err := os.Mkdir(driver.lockFilePath("directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.AcquireLock("directory", "owner", time.Minute); err == nil {
		t.Fatalf("目录不能被当成租约: %v", err)
	}
	if _, err := driver.ReleaseLock("directory", "owner"); !errors.Is(err, ErrUnsafeCacheEntry) {
		t.Fatalf("释放入口未拒绝目录: %v", err)
	}
	if _, err := driver.RenewLock("directory", "owner", time.Minute); !errors.Is(err, ErrUnsafeCacheEntry) {
		t.Fatalf("续期入口未拒绝目录: %v", err)
	}
	if err := os.WriteFile(driver.lockFilePath("oversized"), []byte(strings.Repeat("x", maxFileLockBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.AcquireLock("oversized", "owner", time.Minute); !errors.Is(err, ErrCacheEntryTooLarge) {
		t.Fatalf("超大磁盘租约未被拒绝: %v", err)
	}
}
