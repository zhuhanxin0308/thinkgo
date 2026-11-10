package command

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	framework "github.com/zhuhanxin0308/thinkgo/v3"
)

func TestMakeAppRollbackPreservesExternalChanges(t *testing.T) {
	for _, edited := range []string{"app/admin/controller/index.go", "app/autoload_generated.go", ""} {
		t.Run(edited, func(t *testing.T) {
			base := t.TempDir()
			writeGeneratorTestModule(t, base)
			app := framework.NewAppUninitialized(base)
			t.Cleanup(func() { _ = app.Close() })
			if err := buildNativeApplication(app, "index"); err != nil {
				t.Fatal(err)
			}
			rootGenerated := filepath.Join(base, "app", applicationDiscoveryFilename)
			before, err := os.ReadFile(rootGenerated)
			if err != nil {
				t.Fatal(err)
			}
			original := writeControllerDiscoveryStagedSource
			t.Cleanup(func() { writeControllerDiscoveryStagedSource = original })
			injected := errors.New("测试发现文件暂存失败")
			writeControllerDiscoveryStagedSource = func(root *os.Root, path string, content []byte, mode os.FileMode) error {
				if edited != "" {
					if err := root.WriteFile(filepath.FromSlash(edited), []byte("// 外部编辑器保存的业务变更\n"), 0644); err != nil {
						return err
					}
				}
				return injected
			}
			buildErr := buildNativeApplication(app, "admin")
			if !errors.Is(buildErr, injected) {
				t.Fatalf("未经过受控失败点: %v", buildErr)
			}
			if edited != "" {
				content, err := os.ReadFile(filepath.Join(base, filepath.FromSlash(edited)))
				if err != nil || !strings.Contains(string(content), "外部编辑器") {
					t.Fatalf("回滚丢失了外部编辑: %q %v", content, err)
				}
				if strings.Contains(edited, "/controller/") {
					if !errors.Is(buildErr, ErrControllerDiscoveryRollbackConflict) {
						t.Fatalf("未报告新文件所有权冲突: %v", buildErr)
					}
					backups, err := filepath.Glob(filepath.Join(base, filepath.FromSlash(edited)) + ".discovery-original-*")
					if err != nil || len(backups) != 1 {
						t.Fatalf("未保留原生成内容: %v %v", backups, err)
					}
				}
			} else {
				after, err := os.ReadFile(rootGenerated)
				if err != nil || string(after) != string(before) {
					t.Fatalf("无冲突回滚改变既有生成文件: %v", err)
				}
				if _, err := os.Stat(filepath.Join(base, "app", "admin")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("无冲突回滚留下新应用: %v", err)
				}
			}
		})
	}
}

func TestMakeAppRollbackPreservesEditedPublishedDiscovery(t *testing.T) {
	base := t.TempDir()
	writeGeneratorTestModule(t, base)
	app := framework.NewAppUninitialized(base)
	t.Cleanup(func() { _ = app.Close() })
	if err := buildNativeApplication(app, "index"); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join("app", applicationDiscoveryFilename)
	before, err := os.ReadFile(filepath.Join(base, target))
	if err != nil {
		t.Fatal(err)
	}
	original := linkControllerDiscoveryPublication
	t.Cleanup(func() { linkControllerDiscoveryPublication = original })
	linkControllerDiscoveryPublication = func(root *os.Root, oldPath, newPath string) error {
		if err := original(root, oldPath, newPath); err != nil {
			return err
		}
		if newPath == target && strings.Contains(oldPath, ".discovery-tmp-") {
			return root.WriteFile(newPath, []byte("// 外部编辑器修改已发布的生成文件\n"), 0644)
		}
		return nil
	}
	if err := buildNativeApplication(app, "admin"); !errors.Is(err, ErrControllerDiscoveryRollbackConflict) {
		t.Fatalf("已发布发现文件修改未报告回滚冲突: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(base, target))
	if err != nil || !strings.Contains(string(after), "外部编辑器") {
		t.Fatalf("外部版本被回滚覆盖: %q %v", after, err)
	}
	backups, err := filepath.Glob(filepath.Join(base, target) + ".discovery-backup-*")
	if err != nil || len(backups) != 1 {
		t.Fatalf("旧生成文件备份缺失: %v %v", backups, err)
	}
	backup, err := os.ReadFile(backups[0])
	if err != nil || string(backup) != string(before) {
		t.Fatalf("保留的旧版本不完整: %q %v", backup, err)
	}
}
