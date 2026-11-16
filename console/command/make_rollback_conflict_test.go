package command

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeneratorRollbackPreservesExternalSource(t *testing.T) {
	for _, change := range []string{"没有修改", "并发编辑", "相同字节替换"} {
		t.Run(change, func(t *testing.T) {
			app, _ := prepareControllerDiscoveryTransactionFixture(t, false)
			target := generatorTarget{application: "index", name: "Profile"}
			filename := "profile.go"
			path := filepath.Join(app.BasePath, "app", "index", "controller", filename)
			source := []byte("package controller\n\ntype Profile struct{}\nfunc (*Profile) Index() string { return \"profile\" }\n")
			refreshErr := errors.New("注入发现文件发布失败")
			originalLink := linkControllerDiscoveryPublication
			var external []byte
			linkControllerDiscoveryPublication = func(root *os.Root, original, destination string) error {
				if !strings.Contains(original, ".discovery-tmp-") {
					return originalLink(root, original, destination)
				}
				switch change {
				case "并发编辑":
					external = []byte("package controller\n\n// 用户已编辑\n")
					if err := os.WriteFile(path, external, 0o644); err != nil {
						return err
					}
				case "相同字节替换":
					var err error
					external, err = os.ReadFile(path)
					if err != nil {
						return err
					}
					if err := os.Rename(path, path+".before"); err != nil {
						return err
					}
					if err := os.WriteFile(path, external, 0o644); err != nil {
						return err
					}
				}
				return refreshErr
			}
			t.Cleanup(func() { linkControllerDiscoveryPublication = originalLink })
			err := writeAndRefreshGeneratedControllerSource(app, target, filename, source)
			if !errors.Is(err, refreshErr) {
				t.Fatalf("未返回原始刷新错误: %v", err)
			}
			actual, readErr := os.ReadFile(path)
			if change == "没有修改" {
				if !errors.Is(readErr, os.ErrNotExist) {
					t.Fatalf("未删除本次生成的未变更文件: %v", readErr)
				}
			} else if readErr != nil || string(actual) != string(external) || !strings.Contains(err.Error(), filename) {
				t.Fatalf("未保留或未报告外部版本: %q，读取错误 %v，回滚错误 %v", actual, readErr, err)
			}
			assertNoControllerDiscoveryAuxiliaryFiles(t, app.BasePath)
		})
	}
}

func TestCRUDRollbackPreservesOnlyTheExternallyEditedFile(t *testing.T) {
	command, input, basePath := newCRUDCommandFixture(t)
	applicationAPI := filepath.Join(basePath, "app", "index", "api")
	sourcePath := filepath.Join(applicationAPI, "user.go")
	testPath := filepath.Join(applicationAPI, "user_test.go")
	external := []byte("package api\n\n// 用户已编辑\n")
	refreshErr := errors.New("注入 CRUD 发现更新失败")
	originalLink := linkControllerDiscoveryPublication
	linkControllerDiscoveryPublication = func(_ *os.Root, _, _ string) error {
		if err := os.WriteFile(sourcePath, external, 0o644); err != nil {
			return err
		}
		return refreshErr
	}
	t.Cleanup(func() { linkControllerDiscoveryPublication = originalLink })
	err := command.Execute(input, generatorTestOutput())
	if !errors.Is(err, refreshErr) || !errors.Is(err, ErrControllerDiscoveryRollbackConflict) {
		t.Fatalf("刷新错误和回滚冲突均应保留: %v", err)
	}
	if actual, err := os.ReadFile(sourcePath); err != nil || string(actual) != string(external) {
		t.Fatalf("用户修改的 CRUD 源码应保留: %v", err)
	}
	if _, err := os.Stat(testPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("未变化的 CRUD 测试应正常回滚: %v", err)
	}
	assertNoControllerDiscoveryAuxiliaryFiles(t, basePath)
}
