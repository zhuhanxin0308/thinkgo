// Package dotenvtest 创建加载测试所需的私有环境文件夹具。
package dotenvtest

import (
	"os"

	"golang.org/x/sys/windows"
)

// WriteFile 写入测试夹具并移除运行环境给临时目录附带的继承权限。
func WriteFile(path string, contents []byte) error {
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		return err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return err
	}
	acl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}
