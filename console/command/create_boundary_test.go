package command

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestCreateRefusesDirectoryChangedDuringPreparation 验证依赖准备期间出现的用户内容不会被覆盖。
func TestCreateRefusesDirectoryChangedDuringPreparation(t *testing.T) {
	base := t.TempDir()
	prepare := func(context.Context, string, string) error {
		writeArtifactFixture(t, base, "demo/new-content.txt", "user")
		return nil
	}
	if err := createProject(context.Background(), base, "demo", prepare); err == nil {
		t.Fatal("准备期间出现的用户内容被覆盖")
	}
	content, err := os.ReadFile(filepath.Join(base, "demo", "new-content.txt"))
	if err != nil || string(content) != "user" {
		t.Fatalf("用户文件损坏: %s %v", content, err)
	}
	entries, err := os.ReadDir(base)
	if err != nil || len(entries) != 1 || entries[0].Name() != "demo" {
		t.Fatalf("失败创建未清理临时文件: %v %v", entries, err)
	}
}

// TestCreatePreservesFilesDirectoriesAndLinks 验证普通文件、已有子目录及目录链接均不能充当空项目目录。
func TestCreatePreservesFilesDirectoriesAndLinks(t *testing.T) {
	for _, kind := range []string{"file", "directory", "link"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			switch kind {
			case "file":
				writeArtifactFixture(t, base, "demo", "user")
			case "directory":
				if err := os.MkdirAll(filepath.Join(base, "demo", "existing-empty-child"), 0755); err != nil {
					t.Fatal(err)
				}
			case "link":
				if err := os.Symlink(t.TempDir(), filepath.Join(base, "demo")); err != nil {
					t.Skipf("当前宿主不能创建链接: %v", err)
				}
			}
			if err := createProject(context.Background(), base, "demo", func(context.Context, string, string) error {
				t.Error("目标无效时不应准备依赖")
				return nil
			}); err == nil {
				t.Fatal("非空或链接目标未拒绝")
			}
		})
	}
}

// TestCreateCancellationPreservesExistingEmptyDirectory 验证创建中取消不会删除用户原有空目录。
func TestCreateCancellationPreservesExistingEmptyDirectory(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "demo"), 0755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := createProject(ctx, base, "demo", func(context.Context, string, string) error {
		cancel()
		return nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(base, "demo"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("原有空目录没有保持原样: %v %v", entries, err)
	}
}
