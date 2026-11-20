package command

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/console"
)

// TestVendorPublishRestrictsConfigurationPermissions 验证新建与强制替换均维持配置私有权限。
func TestVendorPublishRestrictsConfigurationPermissions(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		force    bool
		existing os.FileMode
	}{
		{name: "首次发布"},
		{name: "强制首次发布", force: true},
		{name: "收紧旧权限", force: true, existing: 0o644},
		{name: "保留私有权限", force: true, existing: 0o600},
		{name: "跳过已有文件", existing: 0o640},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			base := t.TempDir()
			app := buildConsoleTestApp(t, base)
			source := filepath.Join(base, "vendor", "acme", "demo", "defaults.json")
			if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
				t.Fatal(err)
			}
			const content = `{"api_key":"test-only"}`
			if err := os.WriteFile(source, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(app.GetConfigPath(), "private"+app.GetConfigExt())
			if testCase.existing != 0 {
				if err := os.WriteFile(target, []byte("existing"), testCase.existing); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(target, testCase.existing); err != nil {
					t.Fatal(err)
				}
			}
			if err := publishPackageConfigFile(app, "acme/demo", "private", "defaults.json", testCase.force,
				console.NewOutputWithWriters(&bytes.Buffer{}, &bytes.Buffer{}, false)); err != nil {
				t.Fatal(err)
			}
			information, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			wantContent, wantMode := content, os.FileMode(0o600)
			if testCase.existing != 0 && !testCase.force {
				wantContent, wantMode = "existing", testCase.existing
			}
			// Windows 的访问边界由 ACL 决定，仍在该平台核验文件内容与发布路径。
			if runtime.GOOS != "windows" && information.Mode().Perm() != wantMode {
				t.Errorf("配置权限错误: got=%o want=%o", information.Mode().Perm(), wantMode)
			}
			actual, err := os.ReadFile(target)
			if err != nil || string(actual) != wantContent {
				t.Errorf("发布内容错误: content=%q err=%v", actual, err)
			}
			leftovers, err := filepath.Glob(target + ".tmp-*")
			if err != nil || len(leftovers) != 0 {
				t.Errorf("发布临时文件未清理: %v %v", leftovers, err)
			}
		})
	}
}
