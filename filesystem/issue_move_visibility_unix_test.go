//go:build unix

package filesystem

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMoveAndCopyPreserveCustomUnixPermissionModes(t *testing.T) {
	permissions := Permissions{FilePublic: 0o660, FilePrivate: 0o640}
	for _, visibility := range []string{VisibilityPrivate, VisibilityPublic} {
		t.Run(visibility, func(t *testing.T) {
			disk, root := newIssue153Disk(t, permissions)
			sourcePath := filepath.Join(root, "source.txt")
			mode := disk.configuration.fileMode(visibility)
			if err := os.WriteFile(sourcePath, []byte("current"), mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(sourcePath, mode); err != nil {
				t.Fatal(err)
			}
			previous := oppositeIssue153Visibility(visibility)
			if err := disk.Write("target.txt", "previous", map[string]interface{}{"visibility": previous}); err != nil {
				t.Fatal(err)
			}
			if err := disk.Move("source.txt", "target.txt"); err != nil {
				t.Fatal(err)
			}
			assertIssue153Visibility(t, disk, "target.txt", visibility)
			for _, test := range []struct {
				name    string
				options map[string]interface{}
				mode    os.FileMode
			}{
				{name: "retained.txt", mode: mode},
				{name: "default.txt", options: map[string]interface{}{"retain_visibility": false}, mode: permissions.FilePrivate},
				{name: "explicit.txt", options: map[string]interface{}{"visibility": previous}, mode: disk.configuration.fileMode(previous)},
			} {
				if err := disk.Copy("target.txt", test.name, test.options); err != nil {
					t.Fatal(err)
				}
				information, err := os.Stat(filepath.Join(root, test.name))
				if err != nil || information.Mode().Perm() != test.mode {
					t.Fatalf("复制输出模式错误: name=%q info=%v expected=%#o err=%v", test.name, information, test.mode, err)
				}
			}
			information, err := os.Stat(filepath.Join(root, "target.txt"))
			if err != nil || information.Mode().Perm() != mode {
				t.Fatalf("移动改变了源对象权限: info=%v expected=%#o err=%v", information, mode, err)
			}
		})
	}
}

func TestMoveOverwritesEmptyDirectoryVisibilityOnUnix(t *testing.T) {
	disk, root := newIssue153Disk(t, Permissions{})
	if err := disk.CreateDirectory("source", map[string]interface{}{"visibility": VisibilityPrivate}); err != nil {
		t.Fatal(err)
	}
	if err := disk.Write("source/child.txt", "current", map[string]interface{}{"visibility": VisibilityPublic}); err != nil {
		t.Fatal(err)
	}
	if err := disk.CreateDirectory("target", map[string]interface{}{"visibility": VisibilityPublic}); err != nil {
		t.Fatal(err)
	}
	if err := disk.Write("target/old.txt", "previous"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "target", "old.txt")); err != nil {
		t.Fatal(err)
	}
	if err := disk.Move("source", "target"); err != nil {
		t.Fatalf("覆盖真实空目录失败: %v", err)
	}
	assertIssue153Visibility(t, disk, "target", VisibilityPrivate)
	assertIssue153Visibility(t, disk, "target/child.txt", VisibilityPublic)
	assertLocalFileContent(t, disk, "target/child.txt", "current")
	if _, exists := snapshotIssue153Visibility(disk)["target/old.txt"]; exists {
		t.Fatal("被替换目录遗留旧后代元数据")
	}
}
