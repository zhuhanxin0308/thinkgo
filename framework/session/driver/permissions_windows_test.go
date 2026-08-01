//go:build windows

package driver

import (
	"errors"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TestWindowsSessionDACLRetriesTransientPermissionContention 验证 DACL 写入仅对可识别的瞬态竞争做有界退避。
func TestWindowsSessionDACLRetriesTransientPermissionContention(t *testing.T) {
	originalSet := windowsSetRestrictedSessionPath
	originalSleep := windowsPermissionRetrySleep
	defer func() {
		windowsSetRestrictedSessionPath = originalSet
		windowsPermissionRetrySleep = originalSleep
	}()

	calls := 0
	var waits []time.Duration
	windowsSetRestrictedSessionPath = func(string, bool) error {
		calls++
		if calls < 3 {
			return windows.ERROR_ACCESS_DENIED
		}
		return nil
	}
	windowsPermissionRetrySleep = func(wait time.Duration) {
		waits = append(waits, wait)
	}
	if err := restrictSessionPath("ignored", false); err != nil {
		t.Fatalf("瞬态 DACL 竞争重试后应成功: %v", err)
	}
	if calls != 3 || len(waits) != 2 || waits[0] != fileSessionPermissionInitialBackoff || waits[1] != 2*fileSessionPermissionInitialBackoff {
		t.Fatalf("DACL 退避序列错误: calls=%d waits=%v", calls, waits)
	}
}

// TestWindowsSessionDACLDoesNotRetryPermanentErrors 验证不可识别的安全错误不会被重试或改变错误语义。
func TestWindowsSessionDACLDoesNotRetryPermanentErrors(t *testing.T) {
	originalSet := windowsSetRestrictedSessionPath
	originalSleep := windowsPermissionRetrySleep
	defer func() {
		windowsSetRestrictedSessionPath = originalSet
		windowsPermissionRetrySleep = originalSleep
	}()

	permanent := errors.New("permanent DACL error")
	calls := 0
	waits := 0
	windowsSetRestrictedSessionPath = func(string, bool) error {
		calls++
		return permanent
	}
	windowsPermissionRetrySleep = func(time.Duration) {
		waits++
	}
	if err := restrictSessionPath("ignored", false); !errors.Is(err, permanent) {
		t.Fatalf("永久 DACL 错误应原样返回: %v", err)
	}
	if calls != 1 || waits != 0 {
		t.Fatalf("永久 DACL 错误不应重试: calls=%d waits=%d", calls, waits)
	}
}

// assertRestrictedSessionDACL 验证 Session 路径禁止继承宽松 ACL，且仅授权当前用户。
func assertRestrictedSessionDACL(t *testing.T, path string) {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatalf("读取 Session 路径 DACL 失败: %v", err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		t.Fatalf("读取 Session 路径 DACL 控制位失败: %v", err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatal("Session 路径必须禁止继承父目录 ACL")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatalf("读取 Session 路径 ACL 失败: %v", err)
	}
	if dacl == nil || dacl.AceCount == 0 {
		t.Fatalf("Session 路径应包含当前用户授权，实际为 %#v", dacl)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatalf("读取当前用户 SID 失败: %v", err)
	}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err = windows.GetAce(dacl, index, &ace); err != nil {
			t.Fatalf("读取 Session 路径第 %d 条 ACE 失败: %v", index, err)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.Equals(user.User.Sid) {
			t.Fatalf("Session 路径第 %d 条 ACE 错误授权给非当前用户 %s", index, sid.String())
		}
	}
}

// TestFileRestrictsWindowsDACL 验证 Session 目录和受管文件均采用当前用户专属 DACL。
func TestFileRestrictsWindowsDACL(t *testing.T) {
	driver, directory := newTestFileDriver(t)
	if err := driver.Write("secure-id", "data"); err != nil {
		t.Fatalf("写入受保护 Session 文件失败: %v", err)
	}
	assertRestrictedSessionDACL(t, directory)
	assertRestrictedSessionDACL(t, driver.sessionFilePath("secure-id"))
}
