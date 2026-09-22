package command

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework/console"
)

// TestOpenAPICommentBuildSelection 验证构建标签、测试包和嵌套模块不会混入当前目标文档。
func TestOpenAPICommentBuildSelection(t *testing.T) {
	_, base := newOpenAPICommandFixture(t)
	writeDiscoveryFixture(t, base, "app/index/api/enabled.go", "//go:build apidoc_test\n\npackage api\n// Enabled 已启用。\nfunc Enabled() {}\n")
	writeDiscoveryFixture(t, base, "app/index/api/excluded.go", "//go:build !apidoc_test\n\npackage api\n// @Typo 不应解析\nfunc Excluded() {}\n")
	writeDiscoveryFixture(t, base, "app/index/api/user_test.go", "package api\n// @Typo 不应解析\nfunc TestOnly() {}\n")
	writeDiscoveryFixture(t, base, "nested/go.mod", "module example.com/nested\n\ngo 1.26.6\n")
	writeDiscoveryFixture(t, base, "nested/main.go", "package main\n// @Typo 不应解析\nfunc main() {}\n")
	beforeMod, _ := os.ReadFile(filepath.Join(base, "go.mod"))
	beforeSum, _ := os.ReadFile(filepath.Join(base, "go.sum"))
	t.Setenv("GOFLAGS", "-tags=apidoc_test")
	source, err := scanOpenAPIComments(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	if len(source.Files) != 2 || source.Handlers["example.com/project/app/index/api.Enabled"].Summary != "已启用。" {
		t.Fatalf("构建目标选择错误: %#v", source.Files)
	}
	afterMod, _ := os.ReadFile(filepath.Join(base, "go.mod"))
	afterSum, _ := os.ReadFile(filepath.Join(base, "go.sum"))
	if string(beforeMod) != string(afterMod) || string(beforeSum) != string(afterSum) {
		t.Fatal("源码扫描改写了用户依赖清单")
	}
}

// TestOpenAPICommentInvalidPackage 验证构建文件无效时不能静默生成缺少接口的清单。
func TestOpenAPICommentInvalidPackage(t *testing.T) {
	for _, source := range []string{
		"//go:build (\n\npackage api\nfunc Invalid() {}\n",
		"package another\nfunc Invalid() {}\n",
		"package api\nfunc Invalid( {}\n",
	} {
		t.Run(source, func(t *testing.T) {
			command, base := newOpenAPICommandFixture(t)
			writeDiscoveryFixture(t, base, "app/index/api/invalid.go", source)
			if err := command.Execute(console.NewInput(), generatorTestOutput()); err == nil {
				t.Fatal("非法源码未阻止文档生成")
			}
			if _, err := os.Stat(filepath.Join(base, openAPICommentsGoPath)); !os.IsNotExist(err) {
				t.Fatal("失败留下了生成文件")
			}
		})
	}
}

// TestOpenAPICommentOwnership 验证首次接入保护用户文件，写入失败也不发布部分清单。
func TestOpenAPICommentOwnership(t *testing.T) {
	for _, path := range []string{openAPICommentsGoPath, openAPICommentsJSONPath} {
		t.Run(path, func(t *testing.T) {
			command, base := newOpenAPICommandFixture(t)
			original := "用户维护的数据"
			if path == openAPICommentsGoPath {
				original = "package apidoc\n// 用户维护的数据\n"
			}
			writeDiscoveryFixture(t, base, path, original)
			if err := command.Execute(console.NewInput(), generatorTestOutput()); err == nil {
				t.Fatal("用户文件被生成器接管")
			}
			current, _ := os.ReadFile(filepath.Join(base, path))
			if string(current) != original {
				t.Fatal("用户文件被覆盖")
			}
		})
	}
	command, base := newOpenAPICommandFixture(t)
	if err := command.Execute(console.NewInput(), generatorTestOutput()); err != nil {
		t.Fatal(err)
	}
	previous, _ := os.ReadFile(filepath.Join(base, openAPICommentsGoPath))
	if err := os.Remove(filepath.Join(base, openAPICommentsJSONPath)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(base, openAPICommentsJSONPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := command.Execute(console.NewInput(), generatorTestOutput()); err == nil {
		t.Fatal("目标目录冲突未拒绝")
	}
	current, _ := os.ReadFile(filepath.Join(base, openAPICommentsGoPath))
	if string(current) != string(previous) {
		t.Fatal("失败覆盖了旧入口")
	}
}

// TestOpenAPICommentGoGenerate 验证生成入口从包目录回到模块根，并实际刷新嵌入的字段说明。
func TestOpenAPICommentGoGenerate(t *testing.T) {
	command, base := newOpenAPICommandFixture(t)
	writeDiscoveryFixture(t, base, "cmd/think/main.go", commentCommandFixture)
	if err := command.Execute(console.NewInput(), generatorTestOutput()); err != nil {
		t.Fatal(err)
	}
	writeDiscoveryFixture(t, base, "app/index/api/user.go", strings.Replace(commentedAPISource, "用户姓名。", "重新生成后的姓名。", 1))
	process := exec.Command("go", "generate", "./internal/apidoc")
	process.Dir = base
	process.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
	if output, err := process.CombinedOutput(); err != nil {
		t.Fatalf("go generate 失败: %v\n%s", err, output)
	}
	content, err := os.ReadFile(filepath.Join(base, openAPICommentsJSONPath))
	if err != nil || !strings.Contains(string(content), "重新生成后的姓名。") {
		t.Fatalf("再生成没有更新说明: %v", err)
	}
	if err := command.Execute(&console.Input{Options: map[string]string{"check": "true"}}, generatorTestOutput()); err != nil {
		t.Fatal(err)
	}
}

const commentCommandFixture = `package main

import (
	"os"
	"github.com/zhuhanxin0308/thinkgo/framework"
	"github.com/zhuhanxin0308/thinkgo/framework/console"
	"github.com/zhuhanxin0308/thinkgo/framework/console/command"
)

func main() {
	base, err := os.Getwd()
	if err != nil { panic(err) }
	if len(os.Args) != 2 || os.Args[1] != command.OpenAPIGenerateSignature { panic("命令参数错误") }
	generator := &command.OpenAPIGenerate{}
	generator.SetApp(&framework.App{BasePath: base})
	if err := generator.Execute(console.NewInput(), console.NewOutput()); err != nil { panic(err) }
}
`
