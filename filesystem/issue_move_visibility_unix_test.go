//go:build unix

package filesystem

import (
	"errors"
	"maps"
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

func TestMoveDirectoryTargetPreservesCommitBoundaryOnUnix(t *testing.T) {
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
	entries, err := os.ReadDir(filepath.Join(root, "target"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("移动目标夹具必须是空目录: entries=%v err=%v", entries, err)
	}
	sourceInformation, err := os.Stat(filepath.Join(root, "source"))
	if err != nil {
		t.Fatal(err)
	}
	targetInformation, err := os.Stat(filepath.Join(root, "target"))
	if err != nil {
		t.Fatal(err)
	}
	before := snapshotIssue153Visibility(disk)
	// os.Root.Rename 与 os.Rename 一致拒绝既有目录目标，即使目标为空。
	err = disk.Move("source", "target")
	if !errors.Is(err, os.ErrExist) || errors.Is(err, ErrFilesystemOperationCommitted) {
		t.Fatalf("既有目录目标必须在提交前被拒绝: %v", err)
	}
	for name, original := range map[string]os.FileInfo{"source": sourceInformation, "target": targetInformation} {
		information, err := os.Stat(filepath.Join(root, name))
		if err != nil || !os.SameFile(original, information) {
			t.Fatalf("被拒绝的移动改变了目录身份: name=%q info=%v err=%v", name, information, err)
		}
	}
	assertLocalFileContent(t, disk, "source/child.txt", "current")
	assertIssue153Visibility(t, disk, "source", VisibilityPrivate)
	assertIssue153Visibility(t, disk, "target", VisibilityPublic)
	if actual := snapshotIssue153Visibility(disk); !maps.Equal(actual, before) {
		t.Fatalf("被拒绝的移动改变了元数据: before=%v actual=%v", before, actual)
	}
	// 外部移除空目标后，Local 的旧记录仍在；真实成功移动必须清理它们。
	if err := os.Remove(filepath.Join(root, "target")); err != nil {
		t.Fatal(err)
	}
	if err := disk.Move("source", "target"); err != nil {
		t.Fatalf("移动到不存在的目标失败: %v", err)
	}
	assertIssue153Visibility(t, disk, "target", VisibilityPrivate)
	assertIssue153Visibility(t, disk, "target/child.txt", VisibilityPublic)
	assertLocalFileContent(t, disk, "target/child.txt", "current")
	if exists, err := disk.DirectoryExists("source"); err != nil || exists {
		t.Fatalf("成功移动后源目录仍存在: exists=%t err=%v", exists, err)
	}
	information, err := os.Stat(filepath.Join(root, "target"))
	if err != nil || !os.SameFile(sourceInformation, information) {
		t.Fatalf("成功移动未保留源目录身份: info=%v err=%v", information, err)
	}
	expected := map[string]string{"target": VisibilityPrivate, "target/child.txt": VisibilityPublic}
	if actual := snapshotIssue153Visibility(disk); !maps.Equal(actual, expected) {
		t.Fatalf("成功移动未替换元数据域: actual=%v expected=%v", actual, expected)
	}
}
