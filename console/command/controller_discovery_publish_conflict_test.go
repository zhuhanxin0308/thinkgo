package command

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDiscoveryPublicationPreservesConcurrentUpdates 覆盖最终核验之后的三个确定性交错。
func TestDiscoveryPublicationPreservesConcurrentUpdates(t *testing.T) {
	for _, timing := range []string{"捕获之前", "同内容替换", "捕获之后", "首次创建"} {
		t.Run(timing, func(t *testing.T) {
			base := t.TempDir()
			const target = "autoload_generated.go"
			const oldContent = "// 原始版本\n"
			const external = "// 外部编辑器版本\n"
			path := filepath.Join(base, target)
			if timing != "首次创建" {
				if err := os.WriteFile(path, []byte(oldContent), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			originalRename := renameControllerDiscoveryPath
			originalLink := linkControllerDiscoveryPublication
			t.Cleanup(func() {
				renameControllerDiscoveryPath = originalRename
				linkControllerDiscoveryPublication = originalLink
			})
			injected := false
			renameControllerDiscoveryPath = func(root *os.Root, oldPath, newPath string) error {
				if (timing == "捕获之前" || timing == "同内容替换") && oldPath == target && strings.Contains(newPath, ".discovery-backup-") {
					injected = true
					content := external
					if timing == "同内容替换" {
						// 保留旧 inode，确保相同内容的新对象也必须被识别为外部版本。
						if err := root.Rename(target, target+".editor-old"); err != nil {
							return err
						}
						content = oldContent
					}
					if err := root.WriteFile(target, []byte(content), 0o600); err != nil {
						return err
					}
				}
				return originalRename(root, oldPath, newPath)
			}
			linkControllerDiscoveryPublication = func(root *os.Root, oldPath, newPath string) error {
				if timing == "捕获之后" || timing == "首次创建" {
					injected = true
					if err := root.WriteFile(target, []byte(external), 0o600); err != nil {
						return err
					}
				}
				return originalLink(root, oldPath, newPath)
			}
			err := replaceGeneratedSourceBatch(base, []generatedApplicationSource{{relativePath: target, source: []byte("// 新生成版本\n")}})
			if !injected || !errors.Is(err, ErrControllerDiscoveryPublishConflict) {
				t.Fatalf("并发修改必须阻止发布成功: injected=%t err=%v", injected, err)
			}
			want := external
			if timing == "同内容替换" {
				want = oldContent
			}
			if current, readErr := os.ReadFile(path); readErr != nil || string(current) != want {
				t.Fatalf("外部版本丢失: %q %v", current, readErr)
			}
			if timing == "捕获之后" {
				backups, globErr := filepath.Glob(path + ".discovery-backup-*")
				if globErr != nil || len(backups) != 1 {
					t.Fatalf("原始备份未保留: %v %v", backups, globErr)
				}
				if content, readErr := os.ReadFile(backups[0]); readErr != nil || string(content) != oldContent || !strings.Contains(err.Error(), filepath.Base(backups[0])) {
					t.Fatalf("错误未提供完整恢复材料: %q %v", content, err)
				}
			} else {
				assertNoControllerDiscoveryAuxiliaryFiles(t, base)
			}
		})
	}
}

// TestDiscoveryPublicationRetainsChangedBackup 验证发布过程中被外部句柄改写的备份不会静默清理。
func TestDiscoveryPublicationRetainsChangedBackup(t *testing.T) {
	base := t.TempDir()
	const target = "autoload_generated.go"
	if err := os.WriteFile(filepath.Join(base, target), []byte("// 原版\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	original := linkControllerDiscoveryPublication
	t.Cleanup(func() { linkControllerDiscoveryPublication = original })
	var backupPath string
	linkControllerDiscoveryPublication = func(root *os.Root, source, destination string) error {
		backups, err := filepath.Glob(filepath.Join(base, target) + ".discovery-backup-*")
		if err != nil || len(backups) != 1 {
			t.Fatalf("未捕获原始文件: %v %v", backups, err)
		}
		backupPath = backups[0]
		if err := os.WriteFile(backupPath, []byte("// 延迟提交的外部版本\n"), 0o600); err != nil {
			return err
		}
		return original(root, source, destination)
	}
	err := replaceGeneratedSourceBatch(base, []generatedApplicationSource{{relativePath: target, source: []byte("// 新生成版本\n")}})
	if !errors.Is(err, ErrControllerDiscoveryPublishConflict) || !strings.Contains(err.Error(), filepath.Base(backupPath)) {
		t.Fatalf("变化的备份必须被保留并报告位置: %v", err)
	}
	if content, err := os.ReadFile(backupPath); err != nil || string(content) != "// 延迟提交的外部版本\n" {
		t.Fatalf("外部备份内容丢失: %q %v", content, err)
	}
}
