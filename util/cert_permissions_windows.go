//go:build windows

package util

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

var reOpenFileProcedure = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReOpenFile")

// restrictPrivateFile 在写入私钥前为当前文件句柄设置受保护 DACL，
// 仅允许当前进程用户访问，避免依赖不具备 ACL 语义的 Unix mode 映射。
func restrictPrivateFile(file *os.File) error {
	reopened, err := reopenFileForDACL(windows.Handle(file.Fd()))
	if err != nil {
		return fmt.Errorf("重新打开私钥句柄失败: %w", err)
	}
	defer windows.CloseHandle(reopened)

	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	entries := []windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.SET_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid),
		},
	}}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return err
	}
	securityInformation := windows.SECURITY_INFORMATION(
		windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION,
	)
	if err := windows.SetSecurityInfo(reopened, windows.SE_FILE_OBJECT, securityInformation, nil, nil, acl, nil); err != nil {
		return fmt.Errorf("设置私钥 DACL 失败: %w", err)
	}
	return nil
}

// reopenFileForDACL 基于同一文件对象重新申请读取和写入 DACL 的权限，
// 避免按路径重开引入文件替换竞态。
func reopenFileForDACL(original windows.Handle) (windows.Handle, error) {
	shareMode := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE)
	result, _, callErr := reOpenFileProcedure.Call(
		uintptr(original),
		uintptr(windows.READ_CONTROL|windows.WRITE_DAC),
		uintptr(shareMode),
		0,
	)
	handle := windows.Handle(result)
	if handle != 0 && handle != windows.InvalidHandle {
		return handle, nil
	}
	if errno, ok := callErr.(syscall.Errno); ok && errno != 0 {
		return windows.InvalidHandle, errno
	}
	return windows.InvalidHandle, windows.ERROR_INVALID_HANDLE
}
