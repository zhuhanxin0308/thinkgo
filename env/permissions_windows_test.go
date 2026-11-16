package env

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestEnvLoadValidatesWindowsFileDACL(t *testing.T) {
	const key = "THINKGO_ACL_TEST"
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	allowed := "(A;;FA;;;" + user.User.Sid.String() + ")(A;;FR;;;SY)(A;;FR;;;BA)"
	for _, test := range []struct {
		name    string
		entries string
		null    bool
		reject  bool
	}{
		{name: "运行账户和系统主体"},
		{name: "允许所有人读取", entries: "(A;;FR;;;WD)", reject: true},
		{name: "允许内置用户写入", entries: "(A;;FW;;;BU)", reject: true},
		{name: "继承来源授权仍对文件生效", entries: "(A;ID;FR;;;AU)", reject: true},
		{name: "只继承给子对象", entries: "(A;OIIO;FR;;;WD)"},
		{name: "明确拒绝其他主体执行", entries: fmt.Sprintf("(D;;%#x;;;WD)", windows.FILE_EXECUTE)},
		{name: "空指针 DACL 允许所有访问", null: true, reject: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), ".env")
			if err := os.WriteFile(file, []byte(key+"=fixture\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			var dacl *windows.ACL
			if !test.null {
				descriptor, err := windows.SecurityDescriptorFromString("D:P" + test.entries + allowed)
				if err != nil {
					t.Fatal(err)
				}
				dacl, _, err = descriptor.DACL()
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := windows.SetNamedSecurityInfo(file, windows.SE_FILE_OBJECT,
				windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
				t.Fatal(err)
			}
			environment := NewEnv()
			environment.data[key] = "previous"
			err := environment.Load(file)
			if test.reject {
				if !errors.Is(err, ErrEnvPermissionsTooOpen) || environment.FileValues()[key] != "previous" {
					t.Fatalf("权限过宽的环境文件应保持原值并报错: %v", err)
				}
			} else if err != nil || environment.FileValues()[key] != "fixture" {
				t.Fatalf("隔离的环境文件应加载成功: %v", err)
			}
		})
	}
}
