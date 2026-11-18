package env

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func verifyEnvFilePermissions(file *os.File, _ os.FileInfo) error {
	// 读取已打开文件对象的 DACL，避免另查路径时校验到其他文件。
	descriptor, err := windows.GetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	acl, _, err := descriptor.DACL()
	if errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND) || (err == nil && acl == nil) {
		return fmt.Errorf("%w: 文件没有限制访问的 DACL", ErrEnvPermissionsTooOpen)
	}
	if err != nil {
		return err
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return err
	}
	current, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	return verifyEnvSecurityDescriptor(owner, acl, current.User.Sid)
}

func verifyEnvSecurityDescriptor(owner *windows.SID, acl *windows.ACL, currentUser *windows.SID) error {
	if acl == nil {
		return fmt.Errorf("%w: 文件没有限制访问的 DACL", ErrEnvPermissionsTooOpen)
	}
	if !isTrustedEnvPrincipal(owner, currentUser) {
		return fmt.Errorf("%w: 文件所有者 %s 不受信任", ErrEnvPermissionsTooOpen, owner)
	}
	for index := uint16(0); index < acl.AceCount; index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, uint32(index), &ace); err != nil {
			return err
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 || ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("%w: 无法确认 DACL 条目类型 %d 的授权边界", ErrEnvPermissionsTooOpen, ace.Header.AceType)
		}
		if ace.Mask == 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !isTrustedEnvPrincipal(sid, currentUser) && !sid.IsWellKnown(windows.WinCreatorOwnerRightsSid) {
			return fmt.Errorf("%w: DACL 授权给未受信的安全主体 %s", ErrEnvPermissionsTooOpen, sid)
		}
	}
	return nil
}

func isTrustedEnvPrincipal(sid, currentUser *windows.SID) bool {
	if sid == nil {
		return false
	}
	return (currentUser != nil && sid.Equals(currentUser)) ||
		sid.IsWellKnown(windows.WinLocalSystemSid) ||
		sid.IsWellKnown(windows.WinBuiltinAdministratorsSid)
}
