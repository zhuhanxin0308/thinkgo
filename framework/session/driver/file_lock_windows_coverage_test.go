//go:build windows

package driver

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowsLockHelperCleanupAndIdentityBranches(t *testing.T) {
	_, directory := newTestFileDriver(t)
	path := filepath.Join(directory, "unidentified.lock")
	handle, err := createLockFile(path)
	if err != nil {
		t.Fatalf("创建未识别锁文件失败: %v", err)
	}
	if err := discardUnidentifiedCreatedLockFile(path, handle); err != nil {
		t.Fatalf("清理未识别锁文件失败: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("未识别锁文件应被清理: %v", err)
	}

	fixturePath := filepath.Join(directory, "identity.lock")
	expected := writeWindowsLockFixture(t, fixturePath, "owner")
	opened, err := openLockFile(fixturePath)
	if err != nil {
		t.Fatalf("打开身份测试锁失败: %v", err)
	}
	if matched, err := windowsLockHandleMatchesOwner(fixturePath, opened, "", expected); err != nil || !matched {
		t.Fatalf("空 owner 应通过身份校验: matched=%t err=%v", matched, err)
	}
	if matched, err := windowsLockHandleMatchesOwner(fixturePath, opened, "wrong", expected); err != nil || matched {
		t.Fatalf("错误 owner 不应通过身份校验: matched=%t err=%v", matched, err)
	}
	if matched, err := windowsLockHandleMatchesOwner(fixturePath, opened, "owner", nil); err != nil || matched {
		t.Fatalf("缺少期望身份时不应通过校验: matched=%t err=%v", matched, err)
	}
	if err := opened.Close(); err != nil {
		t.Fatalf("关闭身份测试锁失败: %v", err)
	}

	invalidPath := filepath.Join(directory, "invalid-payload.lock")
	if err := os.WriteFile(invalidPath, []byte("not-json"), 0o600); err != nil {
		t.Fatalf("创建非法载荷锁失败: %v", err)
	}
	invalidHandle, err := openLockFile(invalidPath)
	if err != nil {
		t.Fatalf("打开非法载荷锁失败: %v", err)
	}
	invalidInfo, err := invalidHandle.Stat()
	if err != nil {
		_ = invalidHandle.Close()
		t.Fatalf("读取非法载荷锁身份失败: %v", err)
	}
	if _, err := windowsLockHandleMatchesOwner(invalidPath, invalidHandle, "owner", invalidInfo); err == nil {
		t.Fatal("非法锁载荷应返回解析错误")
	}
	_ = invalidHandle.Close()

	largePath := filepath.Join(directory, "large-payload.lock")
	if err := os.WriteFile(largePath, []byte(strings.Repeat("x", maxFileSessionLockBytes+1)), 0o600); err != nil {
		t.Fatalf("创建超大锁载荷失败: %v", err)
	}
	largeHandle, err := openLockFile(largePath)
	if err != nil {
		t.Fatalf("打开超大锁载荷失败: %v", err)
	}
	largeInfo, err := largeHandle.Stat()
	if err != nil {
		_ = largeHandle.Close()
		t.Fatalf("读取超大锁载荷身份失败: %v", err)
	}
	if _, err := windowsLockHandleMatchesOwner(largePath, largeHandle, "owner", largeInfo); !errors.Is(err, ErrSessionEntryTooLarge) {
		t.Fatalf("超大锁载荷应返回大小错误: %v", err)
	}
	_ = largeHandle.Close()

	plainErr := errors.New("plain error")
	wrapped := lockPathError("open", fixturePath, plainErr)
	if !errors.Is(wrapped, plainErr) {
		t.Fatalf("普通错误应被路径错误包装: %v", wrapped)
	}
	pathErr := &os.PathError{Op: "read", Path: fixturePath, Err: windows.ERROR_ACCESS_DENIED}
	if got := lockPathError("open", fixturePath, pathErr); got != pathErr {
		t.Fatalf("已有 PathError 应保持原对象: got=%#v", got)
	}
	if _, err := createLockFile(filepath.Join(directory, "bad\x00path")); err == nil {
		t.Fatal("非法 Windows 路径应返回错误")
	}
	if err := fileSessionLockDeadlineError(time.Unix(0, 0), time.Unix(0, 0), nil); !errors.Is(err, ErrSessionLockTimeout) {
		t.Fatalf("截止时间到达时应返回锁超时: %v", err)
	}
}
