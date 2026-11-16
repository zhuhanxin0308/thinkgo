//go:build windows

package driver

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

const restrictedSessionSecurityInformation = windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION

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
	return retrySessionPathPermission(path, func() error {
		return windowsSetRestrictedSessionPath(path, directory)
	})
}

func retrySessionPathPermission(path string, setPermission func() error) error {
	backoff := fileSessionPermissionInitialBackoff
	for attempt := 0; attempt < fileSessionPermissionMaxAttempts; attempt++ {
		err := setPermission()
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
	acl, err := restrictedSessionACL(directory)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, restrictedSessionSecurityInformation, nil, nil, acl, nil)
}

// restrictFileSessionDirectory 复核目录句柄后才设置 DACL，避免通过路径二次定位权限目标。
func restrictFileSessionDirectory(_ *os.Root, path string, expected os.FileInfo) error {
	acl, err := restrictedSessionACL(true)
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return retrySessionPathPermission(path, func() error {
		handle, err := windows.CreateFile(name, windows.READ_CONTROL|windows.WRITE_DAC|windows.FILE_READ_ATTRIBUTES,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
			windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		if err != nil {
			return err
		}
		directory := os.NewFile(uintptr(handle), path)
		defer directory.Close()
		actual, err := directory.Stat()
		if err != nil {
			return err
		}
		if !actual.IsDir() || !os.SameFile(expected, actual) {
			return fmt.Errorf("文件会话目录已变更: %q", path)
		}
		return windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, restrictedSessionSecurityInformation, nil, nil, acl, nil)
	})
}

func restrictedSessionACL(directory bool) (*windows.ACL, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
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
	return windows.ACLFromEntries(entries, nil)
}

// nextSessionPermissionBackoff 计算有界安全描述符重试退避，避免并发初始化时忙等。
func nextSessionPermissionBackoff(current time.Duration) time.Duration {
	next := current * 2
	if next > fileSessionPermissionMaxBackoff {
		return fileSessionPermissionMaxBackoff
	}
	return next
}
