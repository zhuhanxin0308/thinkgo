package driver

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"
)

// newTestFileDriver 创建由 t.TempDir 托管且会自动清理的文件 Session 驱动。
func newTestFileDriver(t *testing.T) (*File, string) {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "session")
	driver, err := NewFile(directory)
	if err != nil {
		t.Fatalf("创建文件 Session 驱动失败: %v", err)
	}
	return driver, directory
}

// TestNewFileStrictlyValidatesAndRestrictsDirectory 验证存储根目录必须有效且采用最小权限。
func TestNewFileStrictlyValidatesAndRestrictsDirectory(t *testing.T) {
	if _, err := NewFile(""); !errors.Is(err, ErrInvalidSessionPath) {
		t.Fatalf("空路径应返回 ErrInvalidSessionPath，实际为 %v", err)
	}
	regularFile := filepath.Join(t.TempDir(), "not-directory")
	if err := os.WriteFile(regularFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("创建普通文件失败: %v", err)
	}
	if _, err := NewFile(regularFile); !errors.Is(err, ErrInvalidSessionPath) {
		t.Fatalf("普通文件路径应被拒绝，实际为 %v", err)
	}

	_, directory := newTestFileDriver(t)
	if runtime.GOOS != "windows" {
		info, err := os.Stat(directory)
		if err != nil {
			t.Fatalf("读取 Session 目录失败: %v", err)
		}
		if permission := info.Mode().Perm(); permission != 0o700 {
			t.Fatalf("Session 目录权限应为 0700，实际为 %04o", permission)
		}
	}
}

// TestFileUsesHashedManagedNamesAndExactReadSemantics 验证原始 ID 不进入路径且缺失与空值可区分。
func TestFileUsesHashedManagedNamesAndExactReadSemantics(t *testing.T) {
	driver, directory := newTestFileDriver(t)
	if value, found, err := driver.Read("missing-id"); err != nil || found || value != "" {
		t.Fatalf("缺失 Session 语义错误: value=%q found=%t err=%v", value, found, err)
	}
	if err := driver.Write("known-id", ""); err != nil {
		t.Fatalf("写入空 Session 内容失败: %v", err)
	}
	value, found, err := driver.Read("known-id")
	if err != nil || !found || value != "" {
		t.Fatalf("空 Session 内容读取错误: value=%q found=%t err=%v", value, found, err)
	}
	if _, err = os.Stat(filepath.Join(directory, "known-id")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("原始 Session ID 不得作为文件名，实际错误为 %v", err)
	}
	managed := driver.sessionFilePath("known-id")
	info, err := os.Stat(managed)
	if err != nil {
		t.Fatalf("哈希 Session 文件不存在: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("Session 文件权限应为 0600，实际为 %04o", info.Mode().Perm())
	}
}

// TestFileRejectsInvalidIDsAcrossAllOperations 验证所有入口统一拒绝路径型或超长 ID。
func TestFileRejectsInvalidIDsAcrossAllOperations(t *testing.T) {
	driver, _ := newTestFileDriver(t)
	invalid := []string{"", "../secret", "bad/id", string(make([]byte, maxSessionIDBytes+1))}
	for _, id := range invalid {
		if _, _, err := driver.Read(id); !errors.Is(err, ErrInvalidSessionID) {
			t.Fatalf("Read(%q) 应拒绝非法 ID，实际为 %v", id, err)
		}
		if err := driver.Write(id, "data"); !errors.Is(err, ErrInvalidSessionID) {
			t.Fatalf("Write(%q) 应拒绝非法 ID，实际为 %v", id, err)
		}
		if err := driver.Delete(id); !errors.Is(err, ErrInvalidSessionID) {
			t.Fatalf("Delete(%q) 应拒绝非法 ID，实际为 %v", id, err)
		}
		if err := driver.Update(id, func(string, bool) (string, bool, error) { return "", false, nil }); !errors.Is(err, ErrInvalidSessionID) {
			t.Fatalf("Update(%q) 应拒绝非法 ID，实际为 %v", id, err)
		}
	}
}

// TestFileAtomicUpdatePreventsLostWrites 验证驱动级读改写在并发请求中保持原子。
func TestFileAtomicUpdatePreventsLostWrites(t *testing.T) {
	driver, _ := newTestFileDriver(t)
	const workers = 40
	var wg sync.WaitGroup
	errorsChannel := make(chan error, workers)
	for index := 0; index < workers; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errorsChannel <- driver.Update("counter", func(current string, found bool) (string, bool, error) {
				value := 0
				if found {
					parsed, err := strconv.Atoi(current)
					if err != nil {
						return "", false, err
					}
					value = parsed
				}
				return strconv.Itoa(value + 1), false, nil
			})
		}()
	}
	wg.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatalf("并发原子更新失败: %v", err)
		}
	}
	value, found, err := driver.Read("counter")
	if err != nil || !found || value != strconv.Itoa(workers) {
		t.Fatalf("原子计数结果错误: value=%q found=%t err=%v", value, found, err)
	}
}

// TestFileRejectsSymlinkAndOversizedEntries 验证受管文件不能借助符号链接越权且读取有硬上限。
func TestFileRejectsSymlinkAndOversizedEntries(t *testing.T) {
	driver, _ := newTestFileDriver(t)
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("secret"), 0o600); err != nil {
		t.Fatalf("创建受保护文件失败: %v", err)
	}
	target := driver.sessionFilePath("linked-id")
	if err := os.Symlink(victim, target); err != nil {
		t.Skipf("当前环境不能创建符号链接: %v", err)
	}
	if _, _, err := driver.Read("linked-id"); !errors.Is(err, ErrUnsafeSessionFile) {
		t.Fatalf("符号链接读取应返回 ErrUnsafeSessionFile，实际为 %v", err)
	}
	if err := driver.Write("linked-id", "overwrite"); !errors.Is(err, ErrUnsafeSessionFile) {
		t.Fatalf("符号链接写入应返回 ErrUnsafeSessionFile，实际为 %v", err)
	}
	if content, err := os.ReadFile(victim); err != nil || string(content) != "secret" {
		t.Fatalf("符号链接写入不应修改目标: content=%q err=%v", content, err)
	}

	oversizedPath := driver.sessionFilePath("oversized")
	oversized := make([]byte, maxFileSessionEntryBytes+1)
	if err := os.WriteFile(oversizedPath, oversized, 0o600); err != nil {
		t.Fatalf("创建超大 Session 文件失败: %v", err)
	}
	if _, _, err := driver.Read("oversized"); !errors.Is(err, ErrSessionEntryTooLarge) {
		t.Fatalf("超大 Session 文件应被拒绝，实际为 %v", err)
	}
}

// TestFileClearAndGCAffectOnlyManagedEntries 验证清理操作不会误删同目录业务文件。
func TestFileClearAndGCAffectOnlyManagedEntries(t *testing.T) {
	driver, directory := newTestFileDriver(t)
	unmanaged := filepath.Join(directory, "keep.txt")
	if err := os.WriteFile(unmanaged, []byte("keep"), 0o600); err != nil {
		t.Fatalf("写入非 Session 文件失败: %v", err)
	}
	if err := driver.Write("old", "data"); err != nil {
		t.Fatalf("写入待回收 Session 失败: %v", err)
	}
	oldTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(driver.sessionFilePath("old"), oldTime, oldTime); err != nil {
		t.Fatalf("设置 Session 修改时间失败: %v", err)
	}
	removed, err := driver.GC(time.Hour)
	if err != nil || removed != 1 {
		t.Fatalf("GC 结果错误: removed=%d err=%v", removed, err)
	}
	if err = driver.Write("remaining", "data"); err != nil {
		t.Fatalf("写入待清空 Session 失败: %v", err)
	}
	if err = driver.Clear(); err != nil {
		t.Fatalf("清空 Session 驱动失败: %v", err)
	}
	if _, found, readErr := driver.Read("remaining"); readErr != nil || found {
		t.Fatalf("Clear 应删除受管 Session: found=%t err=%v", found, readErr)
	}
	if content, readErr := os.ReadFile(unmanaged); readErr != nil || string(content) != "keep" {
		t.Fatalf("清理误伤非 Session 文件: content=%q err=%v", content, readErr)
	}
}
