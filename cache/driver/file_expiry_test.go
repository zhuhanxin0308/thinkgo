package driver

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestFileClearExpiredPreservesOtherData 验证清理确实回收物理文件，且保留永久项、代际和未知文件。
func TestFileClearExpiredPreservesOtherData(t *testing.T) {
	backend, root := newTestFileDriver(t)
	entries := map[string]time.Time{
		"expired":                             time.Now().Add(-time.Hour),
		"future":                              time.Now().Add(time.Hour),
		"permanent":                           {},
		cacheFenceMetadataPrefix + "sequence": time.Now().Add(-time.Hour),
	}
	for key, expiry := range entries {
		if err := backend.writeItemLocked(key, "value", expiry); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(backend.cacheFilePath("corrupt"), []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "unknown.cache"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if ok, err := backend.AcquireLock("job", "owner", time.Minute); err != nil || !ok {
		t.Fatal(err)
	}
	if err := backend.ClearExpired(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(backend.cacheFilePath("expired")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("过期项未物理删除: %v", err)
	}
	for _, key := range []string{"future", "permanent", "corrupt", cacheFenceMetadataPrefix + "sequence"} {
		if _, err := os.Stat(backend.cacheFilePath(key)); err != nil {
			t.Errorf("文件被误删: %s %v", key, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "unknown.cache")); err != nil {
		t.Fatal(err)
	}
	if ok, err := backend.RenewLock("job", "owner", time.Minute); err != nil || !ok {
		t.Fatalf("活动锁被误删: %t %v", ok, err)
	}
}
