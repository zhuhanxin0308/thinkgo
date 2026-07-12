//go:build windows

package util

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TestGenerateCertInRootRestrictsWindowsPrivateKeyDACL 验证 Windows 私钥
// 禁止继承父目录权限，且仅向当前进程用户授予访问权限。
func TestGenerateCertInRootRestrictsWindowsPrivateKeyDACL(t *testing.T) {
	basePath := t.TempDir()
	keyName := filepath.Join("runtime", "key.pem")
	if err := GenerateCertInRoot(basePath, filepath.Join("runtime", "cert.pem"), keyName); err != nil {
		t.Fatalf("生成开发证书失败: %v", err)
	}

	descriptor, err := windows.GetNamedSecurityInfo(
		filepath.Join(basePath, keyName),
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatalf("读取私钥安全描述符失败: %v", err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		t.Fatalf("读取私钥 DACL 控制位失败: %v", err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatal("私钥 DACL 必须禁止继承")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatalf("读取私钥 DACL 失败: %v", err)
	}
	if dacl == nil || dacl.AceCount != 1 {
		t.Fatalf("私钥必须且只能包含一条当前用户授权，实际为 %#v", dacl)
	}

	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		t.Fatalf("读取私钥 ACE 失败: %v", err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatalf("读取当前用户 SID 失败: %v", err)
	}
	sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if !sid.Equals(user.User.Sid) {
		t.Fatalf("私钥 ACE 授权给了非当前用户 %s", sid.String())
	}
}

// TestWindowsPrivateKeyPermissionErrors 验证无效句柄不会被误判为权限收紧成功。
func TestWindowsPrivateKeyPermissionErrors(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "closed-key-*.pem")
	if err != nil {
		t.Fatalf("创建临时私钥失败: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("关闭临时私钥失败: %v", err)
	}
	if err := restrictPrivateFile(file); err == nil {
		t.Fatal("已关闭的私钥句柄应返回权限收紧错误")
	}
	if _, err := reopenFileForDACL(windows.InvalidHandle); err == nil {
		t.Fatal("无效 Windows 句柄不应重开成功")
	}
}

// TestGenerateCertRejectsDifferentWindowsVolumes 验证兼容入口不会将共同根目录
// 错误扩展到两个不同文件系统卷。
func TestGenerateCertRejectsDifferentWindowsVolumes(t *testing.T) {
	if err := GenerateCert(`C:\certificates\cert.pem`, `D:\private\key.pem`); err == nil {
		t.Fatal("跨 Windows 卷的证书与私钥路径应被拒绝")
	}
}
