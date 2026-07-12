//go:build windows

package driver

import (
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"thinkgo/framework/log"
)

// assertRestrictedDACL 验证对象不继承宽松 ACL，且仅保留当前用户的一条授权记录。
func assertRestrictedDACL(t *testing.T, path string) {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatalf("读取 %s 的安全描述符失败: %v", path, err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		t.Fatalf("读取 %s 的 DACL 控制位失败: %v", path, err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("%s 的 DACL 必须禁止继承", path)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatalf("读取 %s 的 DACL 失败: %v", path, err)
	}
	if dacl == nil || dacl.AceCount == 0 {
		t.Fatalf("%s 应包含当前用户授权", path)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatalf("读取当前用户 SID 失败: %v", err)
	}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			t.Fatalf("读取 %s 的第 %d 条 ACE 失败: %v", path, index, err)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.Equals(user.User.Sid) {
			t.Fatalf("%s 的第 %d 条 ACE 授权给了非当前用户 %s", path, index, sid.String())
		}
	}
}

// TestFileDriverRestrictsWindowsDACL 验证 Windows 下目录和日志文件均使用受保护 DACL。
func TestFileDriverRestrictsWindowsDACL(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "secure-logs")
	driver, err := NewFileWithOptions(directory, FileOptions{})
	if err != nil {
		t.Fatalf("创建文件日志驱动失败: %v", err)
	}
	t.Cleanup(func() { _ = driver.Close() })

	entryTime := time.Now()
	if err := driver.WriteEntry(&log.LogEntry{Time: entryTime, Level: "info", Message: "secure"}); err != nil {
		t.Fatalf("写入日志失败: %v", err)
	}
	assertRestrictedDACL(t, directory)
	assertRestrictedDACL(t, selectedLogFile(t, driver, entryTime))
}
