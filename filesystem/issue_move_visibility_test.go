package filesystem

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"testing"
)

func TestMoveReplacesVisibilityAndCopyUsesCurrentObject(t *testing.T) {
	for _, permissions := range []struct {
		name  string
		value Permissions
	}{
		{name: "default"},
		{name: "custom", value: Permissions{FilePublic: 0o660, FilePrivate: 0o640, DirectoryPublic: 0o750, DirectoryPrivate: 0o700}},
	} {
		t.Run(permissions.name, func(t *testing.T) {
			for _, test := range []struct {
				name                string
				visibility          string
				rememberSource      bool
				rememberDestination bool
				createDestination   bool
			}{
				{name: "unremembered private", visibility: VisibilityPrivate, rememberDestination: true, createDestination: true},
				{name: "unremembered public", visibility: VisibilityPublic, rememberDestination: true, createDestination: true},
				{name: "remembered private", visibility: VisibilityPrivate, rememberSource: true, rememberDestination: true, createDestination: true},
				{name: "remembered public", visibility: VisibilityPublic, rememberSource: true, rememberDestination: true, createDestination: true},
				{name: "neither remembered", visibility: VisibilityPrivate, createDestination: true},
				{name: "new destination", visibility: VisibilityPublic, rememberSource: true},
			} {
				t.Run(test.name, func(t *testing.T) {
					disk, root := newIssue153Disk(t, permissions.value)
					if test.rememberSource {
						if err := disk.Write("source.txt", "current", map[string]interface{}{"visibility": test.visibility}); err != nil {
							t.Fatal(err)
						}
					} else if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("current"), disk.configuration.fileMode(test.visibility)); err != nil {
						t.Fatal(err)
					}
					// 未缓存的源遵循当前平台实际 FileInfo 的推导规则，不假定 POSIX 权限位。
					current, err := disk.Visibility("source.txt")
					if err != nil {
						t.Fatal(err)
					}
					previous := oppositeIssue153Visibility(current)
					if test.rememberDestination {
						if err := disk.Write("target.txt", "previous", map[string]interface{}{"visibility": previous}); err != nil {
							t.Fatal(err)
						}
					} else if test.createDestination {
						if err := os.WriteFile(filepath.Join(root, "target.txt"), []byte("previous"), disk.configuration.fileMode(previous)); err != nil {
							t.Fatal(err)
						}
					}
					if err := disk.Move("source.txt", "target.txt"); err != nil {
						t.Fatalf("真实移动失败: %v", err)
					}
					assertLocalFileContent(t, disk, "target.txt", "current")
					assertIssue153Visibility(t, disk, "target.txt", current)
					if exists, err := disk.FileExists("source.txt"); err != nil || exists {
						t.Fatalf("移动后源仍存在: exists=%t err=%v", exists, err)
					}
					metadata := snapshotIssue153Visibility(disk)
					if _, exists := metadata["source.txt"]; exists {
						t.Fatal("移动后遗留源可见性缓存")
					}
					if _, exists := metadata["target.txt"]; exists != test.rememberSource {
						t.Fatalf("目标必须只继承源记录: metadata=%v", metadata)
					}
					for _, copyTest := range []struct {
						name       string
						options    map[string]interface{}
						visibility string
					}{
						{name: "retained.txt", visibility: current},
						{name: "default.txt", options: map[string]interface{}{"retain_visibility": false}, visibility: VisibilityPrivate},
						{name: "explicit.txt", options: map[string]interface{}{"visibility": previous, "retain_visibility": false}, visibility: previous},
					} {
						if err := disk.Copy("target.txt", copyTest.name, copyTest.options); err != nil {
							t.Fatal(err)
						}
						assertLocalFileContent(t, disk, copyTest.name, "current")
						assertIssue153Visibility(t, disk, copyTest.name, copyTest.visibility)
					}
				})
			}
		})
	}
}

func TestMoveDirectoryInvalidatesOldDescendantsAndPreservesSourceTree(t *testing.T) {
	disk, root := newIssue153Disk(t, Permissions{})
	for name, visibility := range map[string]string{"tree": VisibilityPrivate, "target": VisibilityPublic} {
		if err := disk.CreateDirectory(name, map[string]interface{}{"visibility": visibility}); err != nil {
			t.Fatal(err)
		}
	}
	for name, visibility := range map[string]string{
		"tree/child.txt":   VisibilityPublic,
		"target/stale.txt": VisibilityPrivate,
		"tree-old.txt":     VisibilityPublic,
		"target-old.txt":   VisibilityPrivate,
	} {
		if err := disk.Write(name, name, map[string]interface{}{"visibility": visibility}); err != nil {
			t.Fatal(err)
		}
	}
	// 模拟目标已被外部移除，但 Local 仍保有旧目录及后代的元数据。
	if err := os.RemoveAll(filepath.Join(root, "target")); err != nil {
		t.Fatal(err)
	}
	if err := disk.Move("tree", "target", map[string]interface{}{"visibility": VisibilityPublic}); err != nil {
		t.Fatal(err)
	}
	assertIssue153Visibility(t, disk, "target", VisibilityPublic)
	assertIssue153Visibility(t, disk, "target/child.txt", VisibilityPublic)
	assertLocalFileContent(t, disk, "target/child.txt", "tree/child.txt")
	expected := map[string]string{
		"target":           VisibilityPublic,
		"target/child.txt": VisibilityPublic,
		"tree-old.txt":     VisibilityPublic,
		"target-old.txt":   VisibilityPrivate,
	}
	if actual := snapshotIssue153Visibility(disk); !maps.Equal(actual, expected) {
		t.Fatalf("目录移动未替换完整元数据域或误伤相似前缀: actual=%v expected=%v", actual, expected)
	}
}

// 路径域重叠只验证元数据迁移；实际 Move 仍以平台 rename 成功为前提。
func TestMoveVisibilitySnapshotsOverlappingMetadataPaths(t *testing.T) {
	for _, test := range []struct {
		name, source, destination string
		before, after             map[string]string
	}{
		{
			name: "destination contains source", source: "a/b", destination: "a",
			before: map[string]string{"a": VisibilityPublic, "a/old": VisibilityPrivate, "a/b": VisibilityPrivate, "a/b/file": VisibilityPublic, "ab": VisibilityPublic},
			after:  map[string]string{"a": VisibilityPrivate, "a/file": VisibilityPublic, "ab": VisibilityPublic},
		},
		{
			name: "source contains destination", source: "a", destination: "a/b",
			before: map[string]string{"a": VisibilityPrivate, "a/file": VisibilityPublic, "a/b": VisibilityPublic, "ab": VisibilityPrivate},
			after:  map[string]string{"a/b": VisibilityPrivate, "a/b/file": VisibilityPublic, "a/b/b": VisibilityPublic, "ab": VisibilityPrivate},
		},
		{
			name: "same path", source: "a", destination: "a",
			before: map[string]string{"a": VisibilityPrivate, "a/file": VisibilityPublic, "ab": VisibilityPublic},
			after:  map[string]string{"a": VisibilityPrivate, "a/file": VisibilityPublic, "ab": VisibilityPublic},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			disk := &Local{visibilityOverrides: maps.Clone(test.before)}
			disk.moveRememberedVisibility(test.source, test.destination)
			if actual := snapshotIssue153Visibility(disk); !maps.Equal(actual, test.after) {
				t.Fatalf("路径域重叠丢失源快照: actual=%v expected=%v", actual, test.after)
			}
		})
	}
}

func TestMoveVisibilityRespectsCommitBoundary(t *testing.T) {
	for _, test := range []struct {
		name      string
		explicit  bool
		committed bool
	}{
		{name: "rename"},
		{name: "chmod", explicit: true, committed: true},
		{name: "sync", committed: true},
		{name: "sync explicit", explicit: true, committed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			disk, root := newIssue153Disk(t, Permissions{})
			if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("current"), 0o600); err != nil {
				t.Fatal(err)
			}
			current, err := disk.Visibility("source.txt")
			if err != nil {
				t.Fatal(err)
			}
			previous := oppositeIssue153Visibility(current)
			if err := disk.Write("target.txt", "previous", map[string]interface{}{"visibility": previous}); err != nil {
				t.Fatal(err)
			}
			before := snapshotIssue153Visibility(disk)
			failure := errors.New("injected move failure")
			switch test.name {
			case "rename":
				disk.operations.replace = func(*os.Root, string, string) error { return failure }
			case "chmod":
				disk.operations.chmod = func(*os.Root, string, os.FileMode) error { return failure }
			default:
				disk.operations.syncDirectory = func(*os.Root, string) error { return failure }
			}
			options := map[string]interface{}{}
			if test.explicit {
				options["visibility"] = previous
			}
			err = disk.Move("source.txt", "target.txt", options)
			if !errors.Is(err, failure) || errors.Is(err, ErrFilesystemOperationCommitted) != test.committed {
				t.Fatalf("移动提交边界错误: committed=%t err=%v", test.committed, err)
			}
			if !test.committed {
				assertLocalFileContent(t, disk, "source.txt", "current")
				assertLocalFileContent(t, disk, "target.txt", "previous")
				if actual := snapshotIssue153Visibility(disk); !maps.Equal(actual, before) {
					t.Fatalf("失败的 rename 改变了元数据: before=%v actual=%v", before, actual)
				}
				return
			}
			assertLocalFileContent(t, disk, "target.txt", "current")
			if exists, err := disk.FileExists("source.txt"); err != nil || exists {
				t.Fatalf("已提交移动必须移除源: exists=%t err=%v", exists, err)
			}
			if test.name == "sync explicit" {
				current = previous
			}
			assertIssue153Visibility(t, disk, "target.txt", current)
			_, remembered := snapshotIssue153Visibility(disk)["target.txt"]
			if remembered != (test.name == "sync explicit") {
				t.Fatalf("提交后错误遗留了被替换对象的可见性: remembered=%t", remembered)
			}
		})
	}
}

func newIssue153Disk(t *testing.T, permissions Permissions) (*Local, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "storage")
	disk, err := NewLocal(LocalConfig{Root: root, Visibility: VisibilityPrivate, Permissions: permissions})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = disk.Close() })
	return disk, root
}

func assertIssue153Visibility(t *testing.T, disk *Local, name, expected string) {
	t.Helper()
	if actual, err := disk.Visibility(name); err != nil || actual != expected {
		t.Fatalf("路径可见性错误: name=%q actual=%q expected=%q err=%v", name, actual, expected, err)
	}
}

func snapshotIssue153Visibility(disk *Local) map[string]string {
	disk.metadataMu.RLock()
	defer disk.metadataMu.RUnlock()
	return maps.Clone(disk.visibilityOverrides)
}

func oppositeIssue153Visibility(visibility string) string {
	if visibility == VisibilityPrivate {
		return VisibilityPublic
	}
	return VisibilityPrivate
}
