//go:build windows

package driver

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

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
