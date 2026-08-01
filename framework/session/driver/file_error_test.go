package driver

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSessionDriverErrorsExposeStableChineseMessages 验证公开哨兵错误提供稳定且可直接展示的中文信息。
func TestSessionDriverErrorsExposeStableChineseMessages(t *testing.T) {
	testCases := map[error]string{
		ErrInvalidSessionPath:   "会话存储路径非法",
		ErrInvalidSessionID:     "会话 ID 非法",
		ErrUnsafeSessionFile:    "会话文件不安全",
		ErrSessionEntryTooLarge: "会话文件超过大小上限",
		ErrSessionLockTimeout:   "会话文件锁超时",
		ErrInvalidSessionUpdate: "会话原子更新回调非法",
	}
	for sessionErr, expected := range testCases {
		if actual := sessionErr.Error(); actual != expected {
			t.Fatalf("会话驱动错误文本不稳定: expected=%q actual=%q", expected, actual)
		}
	}
}

// TestFileDeleteAndUpdateFailureSemantics 验证删除幂等、回调回滚、原子删除和大小上限。
func TestFileDeleteAndUpdateFailureSemantics(t *testing.T) {
	driver, _ := newTestFileDriver(t)
	if err := driver.Write("target", "original"); err != nil {
		t.Fatalf("写入待删除 Session 失败: %v", err)
	}
	if err := driver.Delete("target"); err != nil {
		t.Fatalf("删除 Session 失败: %v", err)
	}
	if err := driver.Delete("target"); err != nil {
		t.Fatalf("重复删除应保持幂等: %v", err)
	}
	if _, found, err := driver.Read("target"); err != nil || found {
		t.Fatalf("删除后 Session 仍存在: found=%t err=%v", found, err)
	}

	if err := driver.Update("atomic", func(string, bool) (string, bool, error) {
		return "original", false, nil
	}); err != nil {
		t.Fatalf("原子创建 Session 失败: %v", err)
	}
	callbackErr := errors.New("abort update")
	if err := driver.Update("atomic", func(string, bool) (string, bool, error) {
		return "changed", false, callbackErr
	}); !errors.Is(err, callbackErr) {
		t.Fatalf("回调错误应原样返回，实际为 %v", err)
	}
	if value, found, err := driver.Read("atomic"); err != nil || !found || value != "original" {
		t.Fatalf("失败回调污染了原值: value=%q found=%t err=%v", value, found, err)
	}
	if err := driver.Update("atomic", func(string, bool) (string, bool, error) {
		return "", true, nil
	}); err != nil {
		t.Fatalf("原子删除失败: %v", err)
	}
	if _, found, _ := driver.Read("atomic"); found {
		t.Fatal("原子删除后 Session 仍存在")
	}
	if err := driver.Update("missing", func(string, bool) (string, bool, error) {
		return "", true, nil
	}); err != nil {
		t.Fatalf("删除缺失 Session 应幂等: %v", err)
	}
	if err := driver.Update("oversized", func(string, bool) (string, bool, error) {
		return strings.Repeat("x", maxFileSessionEntryBytes+1), false, nil
	}); !errors.Is(err, ErrSessionEntryTooLarge) {
		t.Fatalf("超大原子更新应被拒绝，实际为 %v", err)
	}
	if err := driver.Update("invalid", nil); !errors.Is(err, ErrInvalidSessionUpdate) {
		t.Fatalf("nil 更新回调应返回 ErrInvalidSessionUpdate，实际为 %v", err)
	}
}

// TestFileClearHandlesTempsAndUnsafeManagedEntries 验证仅清理陈旧临时文件并报告伪造受管文件。
func TestFileClearHandlesTempsAndUnsafeManagedEntries(t *testing.T) {
	driver, directory := newTestFileDriver(t)
	oldTemp := filepath.Join(directory, ".tmp-session-old")
	recentTemp := filepath.Join(directory, ".tmp-session-recent")
	for _, path := range []string{oldTemp, recentTemp} {
		if err := os.WriteFile(path, []byte("temp"), 0o600); err != nil {
			t.Fatalf("创建 Session 临时文件失败: %v", err)
		}
	}
	oldTime := time.Now().Add(-2 * fileSessionTempMaxAge)
	if err := os.Chtimes(oldTemp, oldTime, oldTime); err != nil {
		t.Fatalf("设置陈旧临时文件时间失败: %v", err)
	}
	if err := driver.Clear(); err != nil {
		t.Fatalf("清理陈旧临时文件失败: %v", err)
	}
	if _, err := os.Stat(oldTemp); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("陈旧临时文件应删除，实际为 %v", err)
	}
	if _, err := os.Stat(recentTemp); err != nil {
		t.Fatalf("近期临时文件应保留: %v", err)
	}

	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("safe"), 0o600); err != nil {
		t.Fatalf("创建受保护文件失败: %v", err)
	}
	managedLink := driver.sessionFilePath("forged")
	if err := os.Symlink(victim, managedLink); err != nil {
		t.Logf("当前环境不能创建符号链接，跳过伪造受管文件分支: %v", err)
		return
	}
	if err := driver.Clear(); !errors.Is(err, ErrUnsafeSessionFile) {
		t.Fatalf("伪造受管符号链接应返回 ErrUnsafeSessionFile，实际为 %v", err)
	}
	if content, err := os.ReadFile(victim); err != nil || string(content) != "safe" {
		t.Fatalf("清理不应影响符号链接目标: content=%q err=%v", content, err)
	}
}

// TestFileCrossProcessLockWaitsRecoversAndChecksOwner 验证活动锁等待、过期锁恢复和 owner 复核。
func TestFileCrossProcessLockWaitsRecoversAndChecksOwner(t *testing.T) {
	driver, _ := newTestFileDriver(t)
	firstInfo, err := driver.acquireCrossProcessLock("locked", "owner-one")
	if err != nil {
		t.Fatalf("获取首个锁失败: %v", err)
	}
	type lockResult struct {
		info os.FileInfo
		err  error
	}
	result := make(chan lockResult, 1)
	go func() {
		info, acquireErr := driver.acquireCrossProcessLock("locked", "owner-two")
		result <- lockResult{info: info, err: acquireErr}
	}()
	time.Sleep(20 * time.Millisecond)
	if err = driver.releaseCrossProcessLock("locked", "wrong-owner", firstInfo); err != nil {
		t.Fatalf("错误 owner 释放不应报错: %v", err)
	}
	if _, err = os.Stat(driver.lockFilePath("locked")); err != nil {
		t.Fatalf("错误 owner 不得删除锁: %v", err)
	}
	if err = driver.releaseCrossProcessLock("locked", "owner-one", firstInfo); err != nil {
		t.Fatalf("释放首个锁失败: %v", err)
	}
	second := <-result
	if second.err != nil {
		t.Fatalf("等待活动锁后获取失败: %v", second.err)
	}
	if err = driver.releaseCrossProcessLock("locked", "owner-two", second.info); err != nil {
		t.Fatalf("释放第二个锁失败: %v", err)
	}
	if err = driver.releaseCrossProcessLock("locked", "owner-two", second.info); err != nil {
		t.Fatalf("重复释放锁应幂等: %v", err)
	}

	stalePath := driver.lockFilePath("stale")
	if err = os.WriteFile(stalePath, []byte(`{"owner":"old","expire_at":1}`), 0o600); err != nil {
		t.Fatalf("写入过期锁失败: %v", err)
	}
	staleInfo, err := driver.acquireCrossProcessLock("stale", "new-owner")
	if err != nil {
		t.Fatalf("恢复过期锁失败: %v", err)
	}
	if err = driver.releaseCrossProcessLock("stale", "new-owner", staleInfo); err != nil {
		t.Fatalf("释放恢复后的锁失败: %v", err)
	}

	corruptPath := driver.lockFilePath("corrupt")
	if err = os.WriteFile(corruptPath, []byte("not-json"), 0o600); err != nil {
		t.Fatalf("写入损坏锁失败: %v", err)
	}
	if _, err = driver.acquireCrossProcessLock("corrupt", "owner"); !errors.Is(err, ErrUnsafeSessionFile) {
		t.Fatalf("近期损坏锁应被拒绝，实际为 %v", err)
	}
	corruptTime := time.Now().Add(-2 * fileSessionCorruptLockAge)
	if err = os.Chtimes(corruptPath, corruptTime, corruptTime); err != nil {
		t.Fatalf("设置损坏锁时间失败: %v", err)
	}
	recoveredInfo, err := driver.acquireCrossProcessLock("corrupt", "recovered")
	if err != nil {
		t.Fatalf("陈旧损坏锁应可安全恢复: %v", err)
	}
	if err = driver.releaseCrossProcessLock("corrupt", "recovered", recoveredInfo); err != nil {
		t.Fatalf("释放恢复锁失败: %v", err)
	}
}

// TestFileLockErrorsPreservePathAndCause 验证锁文件底层错误保留路径、操作和 errors.Is 语义。
func TestFileLockErrorsPreservePathAndCause(t *testing.T) {
	_, directory := newTestFileDriver(t)
	existingPath := filepath.Join(directory, "existing.lock")
	if err := os.WriteFile(existingPath, []byte("existing"), 0o600); err != nil {
		t.Fatalf("创建已有锁文件失败: %v", err)
	}
	if _, err := createLockFile(existingPath); err == nil {
		t.Fatal("重复创建锁文件应失败")
	} else {
		var pathErr *os.PathError
		if !errors.As(err, &pathErr) {
			t.Fatalf("创建错误应包装为 *os.PathError，实际为 %T: %v", err, err)
		}
		if pathErr.Op == "" || pathErr.Path != existingPath || !errors.Is(err, os.ErrExist) {
			t.Fatalf("创建错误未保留操作、路径或原因: %#v", pathErr)
		}
	}

	missingPath := filepath.Join(directory, "missing.lock")
	if _, err := openLockFile(missingPath); err == nil {
		t.Fatal("打开缺失锁文件应失败")
	} else {
		var pathErr *os.PathError
		if !errors.As(err, &pathErr) {
			t.Fatalf("打开错误应包装为 *os.PathError，实际为 %T: %v", err, err)
		}
		if pathErr.Op == "" || pathErr.Path != missingPath || !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("打开错误未保留操作、路径或原因: %#v", pathErr)
		}
	}
}

// TestReadManagedFileReportsCurrentReplacementType 验证二次路径校验必须报告当前替换对象，而不是已打开的旧文件。
func TestReadManagedFileReportsCurrentReplacementType(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "managed.lock")
	backup := filepath.Join(directory, "managed.lock.old")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("创建旧锁文件失败: %v", err)
	}

	openAndReplace := func(currentPath string) (*os.File, error) {
		handle, err := openLockFile(currentPath)
		if err != nil {
			return nil, err
		}
		if err = os.Rename(currentPath, backup); err != nil {
			_ = handle.Close()
			return nil, err
		}
		if err = os.Mkdir(currentPath, 0o700); err != nil {
			_ = handle.Close()
			return nil, err
		}
		return handle, nil
	}

	_, info, found, err := readManagedFileWithOpen(path, maxFileSessionLockBytes, openAndReplace)
	if !errors.Is(err, ErrUnsafeSessionFile) || found {
		t.Fatalf("路径被目录替换后必须立即拒绝: found=%t err=%v", found, err)
	}
	if info == nil || !info.IsDir() {
		t.Fatalf("错误分类必须携带当前目录身份，实际为 %#v", info)
	}
}

// TestReadManagedFileReportsCurrentRegularReplacementIdentity 验证普通文件替换也返回二次校验看到的当前身份。
func TestReadManagedFileReportsCurrentRegularReplacementIdentity(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "managed.lock")
	backup := filepath.Join(directory, "managed.lock.old")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("创建旧锁文件失败: %v", err)
	}
	openAndReplace := func(currentPath string) (*os.File, error) {
		handle, err := openLockFile(currentPath)
		if err != nil {
			return nil, err
		}
		if err = os.Rename(currentPath, backup); err != nil {
			_ = handle.Close()
			return nil, err
		}
		if err = os.WriteFile(currentPath, []byte("new"), 0o600); err != nil {
			_ = handle.Close()
			return nil, err
		}
		return handle, nil
	}

	_, info, found, err := readManagedFileWithOpen(path, maxFileSessionLockBytes, openAndReplace)
	if !errors.Is(err, ErrUnsafeSessionFile) || found {
		t.Fatalf("普通文件身份被替换后必须拒绝: found=%t err=%v", found, err)
	}
	current, currentErr := os.Lstat(path)
	if currentErr != nil || info == nil || !os.SameFile(info, current) {
		t.Fatalf("错误分类必须携带当前普通文件身份: info=%#v current=%#v err=%v", info, current, currentErr)
	}
}

// TestFileSessionLockRetrySeedProducesBoundedSchedule 验证 owner 哈希只需预计算一次，后续退避保持有界确定性。
func TestFileSessionLockRetrySeedProducesBoundedSchedule(t *testing.T) {
	seed := fileSessionLockRetrySeed("stable-owner")
	first := fileSessionLockRetryDelay(seed, 0)
	second := fileSessionLockRetryDelay(seed, 1)
	maximum := fileSessionLockRetry + time.Duration(fileSessionLockRetrySlots-1)*time.Millisecond
	if first < fileSessionLockRetry || first > maximum || second < fileSessionLockRetry || second > maximum {
		t.Fatalf("锁重试退避越界: first=%v second=%v range=[%v,%v]", first, second, fileSessionLockRetry, maximum)
	}
	if first == second {
		t.Fatalf("相邻重试不应持续落入同一槽位: first=%v second=%v", first, second)
	}
	if repeated := fileSessionLockRetryDelay(seed, 0); repeated != first {
		t.Fatalf("相同 seed 与 attempt 必须稳定: first=%v repeated=%v", first, repeated)
	}
}

// TestFileSessionLockRetryDesynchronizesEqualInitialSlots 验证初始槽相同但 seed 不同的等待者不会永久同步。
func TestFileSessionLockRetryDesynchronizesEqualInitialSlots(t *testing.T) {
	firstSeed := fileSessionLockRetrySeed("first-owner")
	secondSeed := uint32(0)
	found := false
	for candidate := 0; candidate < fileSessionLockRetrySlots*4; candidate++ {
		seed := fileSessionLockRetrySeed(fmt.Sprintf("candidate-%d", candidate))
		if seed != firstSeed && fileSessionLockRetryDelay(seed, 0) == fileSessionLockRetryDelay(firstSeed, 0) {
			secondSeed = seed
			found = true
			break
		}
	}
	if !found {
		t.Fatal("未找到用于验证同初始槽的第二个 seed")
	}
	for attempt := uint32(1); attempt < fileSessionLockRetrySlots; attempt++ {
		if fileSessionLockRetryDelay(firstSeed, attempt) != fileSessionLockRetryDelay(secondSeed, attempt) {
			return
		}
	}
	t.Fatal("同初始槽的不同 seed 在完整退避周期内仍永久同步")
}

// TestFileSessionLockDeadlineGraceAllowsOneMissingLockRetry 验证截止瞬间确认锁缺失时仅允许一次最终获取。
func TestFileSessionLockDeadlineGraceAllowsOneMissingLockRetry(t *testing.T) {
	start := time.Unix(0, 0)
	deadline := start.Add(fileSessionLockWait)
	used := false
	if fileSessionLockCanRetryMissingAtDeadline(start, deadline, true, &used) {
		t.Fatal("截止前不应提前消耗锁缺失宽限")
	}
	if fileSessionLockCanRetryMissingAtDeadline(deadline, deadline, false, &used) {
		t.Fatal("非锁缺失错误不应触发宽限")
	}
	if !fileSessionLockCanRetryMissingAtDeadline(deadline, deadline, true, &used) {
		t.Fatal("截止瞬间确认锁缺失时应允许一次最终获取")
	}
	if fileSessionLockCanRetryMissingAtDeadline(deadline, deadline, true, &used) {
		t.Fatal("锁缺失宽限不得形成第二次重试")
	}
}

type shortSessionWriter struct{}

func (shortSessionWriter) Write(data []byte) (int, error) { return len(data) - 1, nil }

type failingSessionWriter struct{ err error }

func (w failingSessionWriter) Write([]byte) (int, error) { return 0, w.err }

// TestFileHelpersRejectShortWritesAndInvalidManagedNames 验证底层短写与名称识别不会静默成功。
func TestFileHelpersRejectShortWritesAndInvalidManagedNames(t *testing.T) {
	if err := writeSessionData(shortSessionWriter{}, []byte("data")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("短写应返回 io.ErrShortWrite，实际为 %v", err)
	}
	writeErr := errors.New("write failed")
	if err := writeSessionData(failingSessionWriter{err: writeErr}, []byte("data")); !errors.Is(err, writeErr) {
		t.Fatalf("writer 错误应传播，实际为 %v", err)
	}
	valid := hashedSessionFilename("id", ".session")
	for name, expected := range map[string]bool{
		valid: true, "other.txt": false, "sess_short.session": false,
		"sess_" + strings.Repeat("z", 64) + ".session": false,
	} {
		if actual := isManagedSessionFilename(name); actual != expected {
			t.Fatalf("受管文件名识别错误: name=%q actual=%t expected=%t", name, actual, expected)
		}
	}
	if ignoreSessionNotExist(os.ErrNotExist) != nil {
		t.Fatal("os.ErrNotExist 应被忽略")
	}
	if err := ignoreSessionNotExist(writeErr); !errors.Is(err, writeErr) {
		t.Fatalf("非 NotExist 错误应保留，实际为 %v", err)
	}
}
