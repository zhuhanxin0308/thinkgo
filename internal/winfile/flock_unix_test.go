//go:build (linux && !android) || (darwin && !ios)

package winfile

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// TestUnixFileDescriptorBounds 验证转换不会截断负值哨兵或超出系统描述符范围的值。
func TestUnixFileDescriptorBounds(t *testing.T) {
	for _, descriptor := range []uintptr{0, math.MaxInt32} {
		converted, err := unixFileDescriptor(descriptor)
		if err != nil || uintptr(converted) != descriptor {
			t.Fatalf("合法描述符转换错误: %d -> %d, %v", descriptor, converted, err)
		}
	}
	for _, descriptor := range []uintptr{uintptr(math.MaxInt32) + 1, ^uintptr(0)} {
		if _, err := unixFileDescriptor(descriptor); !errors.Is(err, os.ErrInvalid) {
			t.Fatalf("越界描述符未被拒绝: %d, %v", descriptor, err)
		}
	}
}

// TestFlockRejectsUnavailableHandles 验证空句柄和已关闭句柄不会进入系统锁调用。
func TestFlockRejectsUnavailableHandles(t *testing.T) {
	if err := Flock(nil, unix.LOCK_EX); !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("空句柄错误丢失: %v", err)
	}
	handle, err := os.CreateTemp(t.TempDir(), "closed-lock-")
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []int{unix.LOCK_EX | unix.LOCK_NB, unix.LOCK_UN} {
		// RawConn.Control 返回底层关闭错误，不承诺转换为 os.ErrClosed。
		if err := Flock(handle, operation); err == nil {
			t.Fatal("关闭句柄被用于系统锁操作")
		}
	}
	if locked, err := tryGuardLock(handle); locked || err == nil {
		t.Fatalf("关闭的 guard 被视为有效锁: locked=%t, %v", locked, err)
	}
	if err := unlockGuard(handle); err == nil {
		t.Fatal("关闭的 guard 解锁错误丢失")
	}
}

// TestFlockPreservesExclusiveOwnership 验证真实内核锁竞争、解锁及系统调用错误均被保留。
func TestFlockPreservesExclusiveOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.guard")
	first, err := openGuard(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := openGuard(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if err := Flock(first, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if err := Flock(second, unix.LOCK_EX|unix.LOCK_NB); !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
		t.Fatalf("竞争句柄绕过了排他锁: %v", err)
	}
	if err := Flock(first, unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err := Flock(second, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatalf("解锁后仍无法获取所有权: %v", err)
	}
	const invalidLockOperation = 0
	descriptor, err := unixFileDescriptor(second.Fd())
	if err != nil {
		t.Fatal(err)
	}
	// Linux 与 macOS 对无效操作返回不同错误，以当前内核的原始结果验证包装层没有改写错误。
	expectedErr := unix.Flock(descriptor, invalidLockOperation)
	if expectedErr == nil {
		t.Fatal("系统未拒绝无效锁操作")
	}
	if err := Flock(second, invalidLockOperation); !errors.Is(err, expectedErr) {
		t.Fatalf("无效锁操作未保留系统错误: actual=%v expected=%v", err, expectedErr)
	}
}
