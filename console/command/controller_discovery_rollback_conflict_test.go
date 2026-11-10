package command

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDiscoveryRollbackPreservesExternalVersions 在后续发布失败前确定性修改已发布文件。
func TestDiscoveryRollbackPreservesExternalVersions(t *testing.T) {
	for _, existed := range []bool{false, true} {
		for _, change := range []string{"写入", "相同内容替换", "删除"} {
			t.Run(change+"/"+map[bool]string{false: "首次生成", true: "已有文件"}[existed], func(t *testing.T) {
				application, paths := prepareControllerDiscoveryTransactionFixture(t, existed)
				var original []byte
				if existed {
					var err error
					original, err = os.ReadFile(paths[0])
					if err != nil {
						t.Fatal(err)
					}
				}
				addControllerDiscoveryTransactionTypes(t, application.BasePath)
				second, err := filepath.Rel(application.BasePath, paths[1])
				if err != nil {
					t.Fatal(err)
				}
				injected := errors.New("注入后续文件发布失败")
				originalLink := linkControllerDiscoveryPublication
				external := []byte("// 外部进程新修改\n")
				linkControllerDiscoveryPublication = func(root *os.Root, oldPath, newPath string) error {
					if strings.Contains(oldPath, ".discovery-tmp-") && newPath == second {
						switch change {
						case "写入":
							if err := os.WriteFile(paths[0], external, 0o644); err != nil {
								return err
							}
						case "相同内容替换":
							var err error
							external, err = os.ReadFile(paths[0])
							if err != nil {
								return err
							}
							if err := os.Rename(paths[0], paths[0]+".external-original"); err != nil {
								return err
							}
							if err := os.WriteFile(paths[0], external, 0o644); err != nil {
								return err
							}
						case "删除":
							if err := os.Remove(paths[0]); err != nil {
								return err
							}
						}
						return injected
					}
					return originalLink(root, oldPath, newPath)
				}
				t.Cleanup(func() { linkControllerDiscoveryPublication = originalLink })
				err = RefreshControllerDiscovery(application)
				if !errors.Is(err, injected) {
					t.Fatalf("未传播原始发布错误: %v", err)
				}
				current, readErr := os.ReadFile(paths[0])
				if change == "删除" {
					if !errors.Is(readErr, os.ErrNotExist) {
						t.Fatalf("外部删除被回滚覆盖: %v", readErr)
					}
				} else if readErr != nil || string(current) != string(external) {
					t.Fatalf("回滚丢失外部版本: %q, %v", current, readErr)
				}
				if !errors.Is(err, ErrControllerDiscoveryRollbackConflict) {
					t.Fatalf("未明确报告外部版本冲突: %v", err)
				}
				if existed {
					backups, globErr := filepath.Glob(paths[0] + ".discovery-backup-*")
					if globErr != nil || len(backups) != 1 {
						t.Fatalf("原始备份未保留: %v, %v", backups, globErr)
					}
					content, readErr := os.ReadFile(backups[0])
					if readErr != nil || string(content) != string(original) || !strings.Contains(err.Error(), filepath.Base(backups[0])) {
						t.Fatalf("错误未提供可恢复的原备份: %v", err)
					}
				}
			})
		}
	}
}

// TestDiscoveryRollbackDoesNotOverwriteNewTarget 验证恢复备份时原子拒绝新出现的目标。
func TestDiscoveryRollbackDoesNotOverwriteNewTarget(t *testing.T) {
	for _, afterCapture := range []bool{false, true} {
		application, paths := prepareControllerDiscoveryTransactionFixture(t, true)
		originals := readControllerDiscoverySnapshots(t, paths)
		addControllerDiscoveryTransactionTypes(t, application.BasePath)
		second, err := filepath.Rel(application.BasePath, paths[1])
		if err != nil {
			t.Fatal(err)
		}
		originalRename := renameControllerDiscoveryPath
		originalLink := linkControllerDiscoveryPublication
		injected := errors.New("注入目标发布失败")
		external := []byte("// 回滚恢复前创建的新目标\n")
		target := paths[1]
		if afterCapture {
			target = paths[0]
		}
		linkControllerDiscoveryPublication = func(root *os.Root, oldPath, newPath string) error {
			if strings.Contains(oldPath, ".discovery-tmp-") && newPath == second {
				if !afterCapture {
					if err := os.WriteFile(target, external, 0o644); err != nil {
						return err
					}
				}
				return injected
			}
			return originalLink(root, oldPath, newPath)
		}
		renameControllerDiscoveryPath = func(root *os.Root, oldPath, newPath string) error {
			if err := originalRename(root, oldPath, newPath); err != nil {
				return err
			}
			if afterCapture && strings.Contains(newPath, ".discovery-rollback-") {
				return os.WriteFile(target, external, 0o644)
			}
			return nil
		}
		err = RefreshControllerDiscovery(application)
		renameControllerDiscoveryPath = originalRename
		linkControllerDiscoveryPublication = originalLink
		if !errors.Is(err, injected) || !errors.Is(err, ErrControllerDiscoveryRollbackConflict) {
			t.Fatalf("新目标应阻止覆盖恢复: %v", err)
		}
		content, readErr := os.ReadFile(target)
		if readErr != nil || string(content) != string(external) {
			t.Fatalf("外部新目标被覆盖: %q, %v", content, readErr)
		}
		backups, globErr := filepath.Glob(target + ".discovery-backup-*")
		if globErr != nil || len(backups) != 1 {
			t.Fatalf("冲突原备份未保留: %v, %v", backups, globErr)
		}
		backup, readErr := os.ReadFile(backups[0])
		if readErr != nil || string(backup) != string(originals[target]) {
			t.Fatal("原备份损坏", readErr)
		}
		if afterCapture {
			captured, err := filepath.Glob(target + ".discovery-rollback-*")
			if err != nil || len(captured) != 1 {
				t.Fatalf("冲突捕获版本未保留: %v, %v", captured, err)
			}
		}
	}
}

// TestDiscoveryRollbackRejectsSymlink 验证回滚不会沿外部替换的符号链接读取或删除外部文件。
func TestDiscoveryRollbackRejectsSymlink(t *testing.T) {
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	const target = "generated.go"
	if err := os.WriteFile(filepath.Join(directory, target), []byte("generated"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := root.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.go")
	if err := os.WriteFile(outside, []byte("external"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := root.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := root.Symlink(outside, target); err != nil {
		t.Skipf("当前系统不允许创建测试符号链接: %v", err)
	}
	staged := []stagedControllerDiscoverySource{{relativePath: target, published: true, publishedInfo: info, publishedContent: []byte("generated")}}
	if err := rollbackControllerDiscoverySources(root, staged); !errors.Is(err, ErrControllerDiscoveryRollbackConflict) {
		t.Fatalf("符号链接替换未被拒绝: %v", err)
	}
	if content, err := os.ReadFile(outside); err != nil || string(content) != "external" {
		t.Fatalf("回滚触及外部文件: %q, %v", content, err)
	}
	if info, err := root.Lstat(target); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("外部符号链接被移除", err)
	}
}

// TestDiscoveryRollbackPreservesCaptureWindowChange 验证核验之后、更名捕获之前的修改仍可恢复。
func TestDiscoveryRollbackPreservesCaptureWindowChange(t *testing.T) {
	application, paths := prepareControllerDiscoveryTransactionFixture(t, true)
	addControllerDiscoveryTransactionTypes(t, application.BasePath)
	second, err := filepath.Rel(application.BasePath, paths[1])
	if err != nil {
		t.Fatal(err)
	}
	originalRename := renameControllerDiscoveryPath
	originalLink := linkControllerDiscoveryPublication
	injected := errors.New("发布失败后进入回滚")
	external := []byte("// 核验后写入的外部内容\n")
	captured := false
	linkControllerDiscoveryPublication = func(root *os.Root, oldPath, newPath string) error {
		if strings.Contains(oldPath, ".discovery-tmp-") && newPath == second {
			return injected
		}
		return originalLink(root, oldPath, newPath)
	}
	renameControllerDiscoveryPath = func(root *os.Root, oldPath, newPath string) error {
		if strings.Contains(newPath, ".discovery-rollback-") {
			captured = true
			if err := os.WriteFile(paths[0], external, 0o644); err != nil {
				return err
			}
		}
		return originalRename(root, oldPath, newPath)
	}
	t.Cleanup(func() {
		renameControllerDiscoveryPath = originalRename
		linkControllerDiscoveryPublication = originalLink
	})
	err = RefreshControllerDiscovery(application)
	if !captured || !errors.Is(err, injected) || !strings.Contains(err.Error(), "回滚冲突") {
		t.Fatalf("未覆盖捕获窗口冲突: captured=%t, err=%v", captured, err)
	}
	content, err := os.ReadFile(paths[0])
	if err != nil || string(content) != string(external) {
		t.Fatalf("捕获窗口外部内容未保留: %q, %v", content, err)
	}
}
