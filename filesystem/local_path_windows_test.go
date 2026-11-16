package filesystem

import (
	"errors"
	"testing"
)

// TestLocalRejectsWindowsPathAliases 验证 Win32 路径别名无法访问、覆盖或删除原文件。
func TestLocalRejectsWindowsPathAliases(t *testing.T) {
	const target = "users/victim.txt"
	for _, path := range []string{"users/...", "users/.... ", "users/victim.txt.", "users/victim.txt ", "users/victim.txt:stream", "users/NUL.txt", "users/CoM1.log"} {
		t.Run(path, func(t *testing.T) {
			disk, err := NewLocal(LocalConfig{Root: t.TempDir(), URL: "/storage"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = disk.Close() })
			if err := disk.Write(target, "原始内容"); err != nil {
				t.Fatal(err)
			}
			if _, err := disk.Has(path); !errors.Is(err, ErrInvalidPath) {
				t.Errorf("存在性检查应拒绝路径别名: %v", err)
			}
			if _, err := disk.DirectoryExists(path); !errors.Is(err, ErrInvalidPath) {
				t.Errorf("目录检查应拒绝路径别名: %v", err)
			}
			if err := disk.Write(path, "覆盖"); !errors.Is(err, ErrInvalidPath) {
				t.Errorf("写入应拒绝路径别名: %v", err)
			}
			if err := disk.DeleteDirectory(path); !errors.Is(err, ErrInvalidPath) {
				t.Errorf("删除应拒绝路径别名: %v", err)
			}
			if _, err := disk.Path(path); !errors.Is(err, ErrInvalidPath) {
				t.Errorf("路径映射应拒绝路径别名: %v", err)
			}
			if _, err := disk.URL(path); !errors.Is(err, ErrInvalidPath) {
				t.Errorf("URL 映射应拒绝路径别名: %v", err)
			}
			if content, err := disk.Read(target); err != nil || content != "原始内容" {
				t.Fatalf("路径别名改变了原文件: %q, %v", content, err)
			}
		})
	}
}

// TestPolicyUploadPreservesFilenameRejectionForAliases 验证路径安全拦截保留旧的上传错误分类。
func TestPolicyUploadPreservesFilenameRejectionForAliases(t *testing.T) {
	disk, _ := policyTestDisk(t)
	options := policyTestOptions(t, 10, map[string][]string{"txt": {"text/plain"}})
	for _, target := range []string{"bad.txt.", "bad.txt "} {
		file := policyTestFile(t, "valid.txt", []byte("hello"), false)
		key, err := disk.PutFileAs("", file, target, options)
		if key != "" || !errors.Is(err, ErrInvalidPath) || !errors.Is(err, ErrUploadTypeNotAllowed) {
			t.Fatalf("别名 %q 应同时保留路径和文件名策略错误: %q %v", target, key, err)
		}
	}
	file := policyTestFile(t, "valid.txt", []byte("hello"), false)
	if _, err := disk.PutFileAs("", file, "../bad.txt", options); !errors.Is(err, ErrInvalidPath) || errors.Is(err, ErrUploadTypeNotAllowed) {
		t.Fatalf("越界路径应保留原有分类: %v", err)
	}
}
