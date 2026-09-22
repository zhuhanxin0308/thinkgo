//go:build windows

package driver

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

const (
	fileSessionPermissionMaxAttempts    = 8
	fileSessionPermissionInitialBackoff = 5 * time.Millisecond
	fileSessionPermissionMaxBackoff     = 40 * time.Millisecond
)

var (
	windowsSetRestrictedSessionPath = setRestrictedSessionPath
	windowsPermissionRetrySleep     = time.Sleep
)

// restrictSessionPath 使用受保护 DACL，仅允许当前进程用户访问 Session 目录或文件。
func restrictSessionPath(path string, directory bool) error {
	backoff := fileSessionPermissionInitialBackoff
	for attempt := 0; attempt < fileSessionPermissionMaxAttempts; attempt++ {
		err := windowsSetRestrictedSessionPath(path, directory)
		if err == nil {
			return nil
		}
		if (!errors.Is(err, windows.ERROR_ACCESS_DENIED) && !errors.Is(err, windows.ERROR_SHARING_VIOLATION)) || attempt == fileSessionPermissionMaxAttempts-1 {
			return &os.PathError{Op: "chmod", Path: path, Err: err}
		}
		windowsPermissionRetrySleep(backoff)
		backoff = nextSessionPermissionBackoff(backoff)
	}
	return nil
}

// setRestrictedSessionPath 执行一次受保护 DACL 设置，不包含重试策略。
func setRestrictedSessionPath(path string, directory bool) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	inheritance := uint32(windows.NO_INHERITANCE)
	if directory {
		inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	entries := []windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.SET_ACCESS,
		Inheritance:       inheritance,
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
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, securityInformation, nil, nil, acl, nil)
}

// nextSessionPermissionBackoff 计算有界安全描述符重试退避，避免并发初始化时忙等。
func nextSessionPermissionBackoff(current time.Duration) time.Duration {
	next := current * 2
	if next > fileSessionPermissionMaxBackoff {
		return fileSessionPermissionMaxBackoff
	}
	return next
}
