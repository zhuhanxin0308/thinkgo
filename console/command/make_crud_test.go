package command

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework"
	"github.com/zhuhanxin0308/thinkgo/framework/console"
)

const crudTestModel = `package model

import "github.com/zhuhanxin0308/thinkgo/framework/db"

// User 是生成器验收使用的真实模型，包含敏感字段与可空字段。
type User struct {
	*db.Model
	ID int64 ` + "`thinkgo:\"id\" json:\"id\"`" + `
	Name string ` + "`thinkgo:\"name\" json:\"name\" validate:\"required|length:2,32\"`" + `
	Active bool ` + "`thinkgo:\"active\" json:\"active\" default:\"true\"`" + `
	Note *string ` + "`thinkgo:\"note\" json:\"note\"`" + `
	Secret string ` + "`thinkgo:\"secret\" json:\"-\"`" + `
	Count int64 ` + "`thinkgo:\"count,readonly\" json:\"count\"`" + `
}

func (*User) ConfigureModel(model *db.Model) error {
	model.Table("users").AutoTimestamp(false)
	return nil
}
`

func newCRUDCommandFixture(t *testing.T) (*MakeCRUD, *console.Input, string) {
	t.Helper()
	basePath := t.TempDir()
	writeGeneratorTestModule(t, basePath)
	writeDiscoveryFixture(t, basePath, "app/index/model/user.go", crudTestModel)
	command := &MakeCRUD{}
	command.Configure()
	command.SetApp(&framework.App{BasePath: basePath})
	input := &console.Input{Args: []string{"User"}, Options: map[string]string{
		"write": "Name,Active,Note", "read": "ID,Name,Active,Note", "path": "/users",
	}}
	return command, input, basePath
}

// TestMakeCRUDProducesRunnableBusinessAPI 验证生成结果在独立模块中编译，并完成真实 SQLite CRUD。
func TestMakeCRUDProducesRunnableBusinessAPI(t *testing.T) {
	command, input, basePath := newCRUDCommandFixture(t)
	if err := command.Execute(input, generatorTestOutput()); err != nil {
		t.Fatal(err)
	}
	source := readGeneratedFile(t, basePath, "app", "index", "api", "user.go")
	if strings.Contains(source, "Secret") || strings.Contains(source, "TODO") || !strings.Contains(source, "binding.Optional") || !strings.Contains(source, "db.Paginate") {
		t.Fatalf("生成结果暴露未选择字段或缺少业务能力:\n%s", source)
	}
	readGeneratedFile(t, basePath, "app", "index", "api", "user_test.go")
	if discovery := readGeneratedFile(t, basePath, "app", "index", applicationDiscoveryFilename); !strings.Contains(discovery, "applicationModel.User") {
		t.Fatal("生成的处理器模型没有进入应用发现清单")
	}
	writeDiscoveryFixture(t, basePath, "app/index/api/business_test.go", crudBusinessTestSource)
	process := exec.Command("go", "test", "-mod=mod", "./...", "-count=1")
	process.Dir = basePath
	process.Env = append(os.Environ(), "GOWORK=off")
	if output, err := process.CombinedOutput(); err != nil {
		t.Fatalf("下游 CRUD 验收失败: %v\n%s", err, output)
	}
}

// TestMakeCRUDRejectsUnsafeSelections 验证字段白名单、主键与输出路径必须在写入前校验。
func TestMakeCRUDRejectsUnsafeSelections(t *testing.T) {
	command, input, basePath := newCRUDCommandFixture(t)
	for _, test := range []struct{ key, value string }{
		{"write", ""}, {"read", ""}, {"path", ""}, {"path", "/users/:id"},
		{"path", "/../users"}, {"read", "Secret"}, {"write", "Count"},
		{"write", "Missing"}, {"read", "ID,ID"}, {"key", "Missing"}, {"key", "Active"},
	} {
		t.Run(test.key+"="+test.value, func(t *testing.T) {
			previous, exists := input.Options[test.key]
			input.Options[test.key] = test.value
			err := command.Execute(input, generatorTestOutput())
			if exists {
				input.Options[test.key] = previous
			} else {
				delete(input.Options, test.key)
			}
			if err == nil {
				t.Fatal("非法生成参数未拒绝")
			}
			if _, err := os.Stat(filepath.Join(basePath, "app", "index", "api", "user.go")); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("非法参数留下了源码: %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	canceled := console.NewInputContext(ctx, "User")
	if err := command.Execute(canceled, generatorTestOutput()); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消命令仍开始生成: %v", err)
	}
}

// TestMakeCRUDPreservesFilesAndRollsBackBatch 验证第二个文件冲突与发现失败都回滚本次新源码。
func TestMakeCRUDPreservesFilesAndRollsBackBatch(t *testing.T) {
	for _, discoveryFailure := range []bool{false, true} {
		command, input, basePath := newCRUDCommandFixture(t)
		if discoveryFailure {
			writeDiscoveryFixture(t, basePath, "app/index/controller/broken.go", "package controller\ntype Broken struct {\n")
		} else {
			writeDiscoveryFixture(t, basePath, "app/index/api/user_test.go", "package api\n// 用户保留的测试。\n")
		}
		if err := command.Execute(input, generatorTestOutput()); err == nil {
			t.Fatal("生成冲突未报告")
		}
		if _, err := os.Stat(filepath.Join(basePath, "app", "index", "api", "user.go")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("失败留下了源码: %v", err)
		}
		if !discoveryFailure {
			if content := readGeneratedFile(t, basePath, "app", "index", "api", "user_test.go"); !strings.Contains(content, "用户保留的测试") {
				t.Fatal("覆盖了用户测试")
			}
		}
	}
}

// TestMakeCRUDRejectsShadowedModelMethods 验证合法模型字段遮蔽 ORM 方法时不会留下无法编译的源码。
func TestMakeCRUDRejectsShadowedModelMethods(t *testing.T) {
	command, input, basePath := newCRUDCommandFixture(t)
	modelSource := strings.Replace(crudTestModel, "\tSecret string", "\tSave string\n\tSecret string", 1)
	writeDiscoveryFixture(t, basePath, "app/index/model/user.go", modelSource)
	if err := command.Execute(input, generatorTestOutput()); err == nil {
		t.Fatal("生成器没有发现 Save 字段遮蔽保存方法")
	}
	if _, err := os.Stat(filepath.Join(basePath, "app", "index", "api", "user.go")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("语义错误留下了源码: %v", err)
	}
}

// TestMakeCRUDInheritsModelComments 验证模型字段注释直接进入请求、PATCH 和输出的文档标签。
func TestMakeCRUDInheritsModelComments(t *testing.T) {
	command, input, basePath := newCRUDCommandFixture(t)
	modelSource := strings.Replace(crudTestModel, "\tName string", "\t// Name 用户显示名称。\n\tName string", 1)
	writeDiscoveryFixture(t, basePath, "app/index/model/user.go", modelSource)
	if err := command.Execute(input, generatorTestOutput()); err != nil {
		t.Fatal(err)
	}
	source := readGeneratedFile(t, basePath, "app", "index", "api", "user.go")
	if strings.Count(source, `doc:"用户显示名称。"`) != 3 {
		t.Fatalf("字段说明没有完整继承: %s", source)
	}
}
