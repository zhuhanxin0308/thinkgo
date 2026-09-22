//go:build windows

package winfile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// TestGuardCannotBeReplacedWhileHeld 验证稳定锁持有期间的删除共享约束，避免替换路径产生第二把锁。
func TestGuardCannotBeReplacedWhileHeld(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "stable.guard")
	err := WithGuard(context.Background(), path, func() error {
		if err := os.Remove(path); !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			t.Fatalf("持有的稳定锁可被删除: %v", err)
		}
		replacement := filepath.Join(directory, "replacement")
		if err := os.WriteFile(replacement, nil, 0o600); err != nil {
			return err
		}
		if err := Replace(replacement, path); err == nil {
			t.Fatal("持有的稳定锁可被替换")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestGuardPreservesRealAccessDenied 用真实 DACL 验证权限失败立即保留路径和原因，不伪装为排队超时。
func TestGuardPreservesRealAccessDenied(t *testing.T) {
	path := filepath.Join(t.TempDir(), "denied.guard")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	original, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	denied, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.FILE_READ_DATA | windows.FILE_WRITE_DATA,
		AccessMode:        windows.DENY_ACCESS,
		Trustee:           windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_USER, TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid)},
	}}, original)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, original, nil); err != nil {
			t.Errorf("恢复测试 ACL 失败: %v", err)
		}
	})
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, denied, nil); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	called := false
	err = WithGuard(context.Background(), path, func() error { called = true; return nil })
	var pathErr *os.PathError
	if called || !errors.Is(err, windows.ERROR_ACCESS_DENIED) || !errors.As(err, &pathErr) || pathErr.Path != path {
		t.Fatalf("真实权限拒绝未保留: called=%t err=%v", called, err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("永久权限错误被误当成锁等待")
	}
}
