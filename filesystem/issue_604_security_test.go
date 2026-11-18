package filesystem

import (
	"errors"
	"io/fs"
	"mime/multipart"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestPutFileAsRejectsCrossDirectoryEscapeAndBackslashExtension 验证严格策略与可信上传下
// PutFileAs 均不允许通过 .. 或路径分隔符逃逸出目标子目录覆写兄弟租户文件，且 uploadHashName 统一规避反斜杠扩展名。
func TestPutFileAsRejectsCrossDirectoryEscapeAndBackslashExtension(t *testing.T) {
	disk, root := policyTestDisk(t)
	if err := disk.Write("tenants/bob/report.txt", "bob-original"); err != nil {
		t.Fatal(err)
	}
	policy, err := NewUploadPolicy(UploadPolicyConfig{
		MaxBytes:     1024,
		AllowedTypes: map[string][]string{"txt": {"text/plain"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	fh := policyTestFile(t, "report.txt", []byte("alice-payload"), false)

	escapeNames := []string{
		"../bob/report.txt",
		`..\bob\report.txt`,
		"sub/../../bob/report.txt",
		`sub\..\..\bob\report.txt`,
		"sub/report.txt",
		`sub\report.txt`,
	}
	for _, name := range escapeNames {
		if key, err := disk.PutFileAs("tenants/alice", fh, name, map[string]interface{}{"upload_policy": policy}); key != "" || !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("严格策略下非法文件名 %q 未被拒绝: key=%q err=%v", name, key, err)
		}
		if key, err := disk.PutFileAs("tenants/alice", fh, name, map[string]interface{}{"trusted_upload": true}); key != "" || !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("可信上传下越界或分段文件名 %q 未被拒绝: key=%q err=%v", name, key, err)
		}
	}
	// 分隔符不含 .. 时在严格策略下同时保留 ErrUploadTypeNotAllowed 分类。
	for _, sepName := range []string{"sub/report.txt", `sub\report.txt`} {
		_, err := disk.PutFileAs("tenants/alice", fh, sepName, map[string]interface{}{"upload_policy": policy})
		if !errors.Is(err, ErrUploadTypeNotAllowed) || !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("严格策略下带分隔符文件名 %q 错误分类不完整: %v", sepName, err)
		}
	}
	if got, err := disk.Read("tenants/bob/report.txt"); err != nil || got != "bob-original" {
		t.Fatalf("兄弟租户文件被篡改: got=%q err=%v", got, err)
	}

	// 验证自定义 HashNameRule 返回 .. 时同样无法越出 directory。
	escapeRule := HashNameRule(func(_ *multipart.FileHeader) (string, error) { return "../bob/report", nil })
	if key, err := disk.PutFile("tenants/alice", fh, escapeRule, map[string]interface{}{"trusted_upload": true}); key != "" || !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("自定义命名规则返回 .. 应被拒绝: key=%q err=%v", key, err)
	}

	// 验证 uploadHashName 对包含反斜杠的客户端文件名跨平台不会把反斜杠拼入扩展名产生子目录。
	backslashHeader := newUploadFileHeader(t, `a.dir\b`, []byte("safe-bytes"))
	generatedName, err := uploadHashName(backslashHeader, nil)
	if err != nil {
		t.Fatalf("uploadHashName 处理反斜杠文件名失败: %v", err)
	}
	if strings.Count(generatedName, "/") != 1 || strings.Contains(generatedName, `\`) || strings.HasSuffix(generatedName, "/b") {
		t.Fatalf("反斜杠被带入生成路径扩展名: %q", generatedName)
	}
	storedKey, err := disk.PutFile("tenants/alice", backslashHeader, nil, map[string]interface{}{"trusted_upload": true})
	if err != nil || !strings.HasPrefix(storedKey, "tenants/alice/") || strings.HasSuffix(storedKey, "/b") {
		t.Fatalf("PutFile 存储键异常: key=%q err=%v", storedKey, err)
	}
	if _, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(storedKey))); statErr != nil {
		t.Fatalf("生成文件未正确落盘: %v", statErr)
	}
}

// TestNewLocalRejectsHostlessURLAndInvalidPermissionMasks 验证 NewLocal 拒绝空主机绝对 URL（防开放重定向）及非法权限位掩码。
func TestNewLocalRejectsHostlessURLAndInvalidPermissionMasks(t *testing.T) {
	root := t.TempDir()
	for _, badURL := range []string{"https://", "http://", "https:///storage", "//", "///evil.com", "/bad%zz", "https:opaque"} {
		if disk, err := NewLocal(LocalConfig{Root: root, URL: badURL}); disk != nil || !errors.Is(err, ErrInvalidConfiguration) {
			if disk != nil {
				_ = disk.Close()
			}
			t.Fatalf("NewLocal 应拒绝非法 URL %q: disk=%v err=%v", badURL, disk, err)
		}
		if _, err := localFileURL(badURL, "evil.com/payload.js"); !errors.Is(err, ErrInvalidConfiguration) {
			t.Fatalf("localFileURL 应拒绝非法前缀 %q: %v", badURL, err)
		}
	}

	invalidPermissions := []Permissions{
		{FilePublic: 0o1000},
		{FilePrivate: fs.ModeSticky | 0o600},
		{DirectoryPublic: 0o2755},
		{DirectoryPrivate: 0o7777},
	}
	for _, perms := range invalidPermissions {
		if disk, err := NewLocal(LocalConfig{Root: root, Permissions: perms}); disk != nil || !errors.Is(err, ErrInvalidConfiguration) {
			if disk != nil {
				_ = disk.Close()
			}
			t.Fatalf("NewLocal 应拒绝越界权限掩码 %#v: err=%v", perms, err)
		}
	}
}

// TestLocalSamePathCopyAppliesExplicitVisibility 验证同路径 Copy 携带显式 visibility 或 retain_visibility=false 时更新文件权限。
func TestLocalSamePathCopyAppliesExplicitVisibility(t *testing.T) {
	root := t.TempDir()
	disk, err := NewLocal(LocalConfig{Root: root, Visibility: VisibilityPublic})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = disk.Close() })

	if err := disk.Write("a.txt", "hello", map[string]interface{}{"visibility": VisibilityPrivate}); err != nil {
		t.Fatal(err)
	}
	if vis, err := disk.Visibility("a.txt"); err != nil || vis != VisibilityPrivate {
		t.Fatalf("初始可见性错误: %q %v", vis, err)
	}
	if err := disk.Copy("./a.txt", "a.txt", map[string]interface{}{"visibility": VisibilityPublic}); err != nil {
		t.Fatalf("同路径显式可见性复制失败: %v", err)
	}
	if vis, err := disk.Visibility("a.txt"); err != nil || vis != VisibilityPublic {
		t.Fatalf("同路径 Copy 丢弃了显式 visibility: got=%q err=%v", vis, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(root, "a.txt"))
		if err != nil || info.Mode().Perm() != defaultFilePublicMode {
			t.Fatalf("Unix 文件权限位未更新: mode=%v err=%v", info.Mode().Perm(), err)
		}
	}

	// 验证 retain_visibility=false 时回退到磁盘默认可见性，且 chmod 错误被传播。
	if err := disk.SetVisibility("a.txt", VisibilityPrivate); err != nil {
		t.Fatal(err)
	}
	if err := disk.Copy("a.txt", "a.txt", map[string]interface{}{"retain_visibility": false}); err != nil {
		t.Fatal(err)
	}
	if vis, err := disk.Visibility("a.txt"); err != nil || vis != VisibilityPublic {
		t.Fatalf("同路径 retain_visibility=false 未恢复默认可见性: %q %v", vis, err)
	}

	chmodErr := errors.New("chmod failed")
	disk.operations.chmod = func(*os.Root, string, os.FileMode) error { return chmodErr }
	if err := disk.Copy("a.txt", "a.txt", map[string]interface{}{"visibility": VisibilityPrivate}); !errors.Is(err, chmodErr) {
		t.Fatalf("同路径 Copy 未传播 chmod 错误: %v", err)
	}
}

// TestLocalDeleteRemovesDanglingSymlink 验证 Delete 使用 Lstat 识别并删除悬空符号链接，使 ListContents 可恢复正常。
func TestLocalDeleteRemovesDanglingSymlink(t *testing.T) {
	root := t.TempDir()
	disk, err := NewLocal(LocalConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = disk.Close() })

	linkPath := filepath.Join(root, "broken_symlink")
	if err := os.Symlink(filepath.Join(root, "nonexistent-target"), linkPath); err != nil {
		t.Skipf("当前环境不支持创建符号链接: %v", err)
	}
	if _, err := disk.ListContents("", false); !errors.Is(err, ErrSymbolicLink) {
		t.Fatalf("删除前悬空链接应触发 ErrSymbolicLink，实际为 %v", err)
	}
	if err := disk.Delete("broken_symlink"); err != nil {
		t.Fatalf("删除悬空符号链接失败: %v", err)
	}
	if _, err := os.Lstat(linkPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Delete 返回成功后悬空符号链接仍残留: %v", err)
	}
	entries, err := disk.ListContents("", false)
	if err != nil || len(entries) != 0 {
		t.Fatalf("删除悬空链接后 ListContents 未恢复: entries=%#v err=%v", entries, err)
	}
}
