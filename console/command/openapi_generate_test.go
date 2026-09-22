package command

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework"
	"github.com/zhuhanxin0308/thinkgo/framework/console"
	"github.com/zhuhanxin0308/thinkgo/framework/openapi"
)

const commentedAPISource = `package api

import "github.com/zhuhanxin0308/thinkgo/framework/binding"

// Input 用户查询条件。
type Input struct {
	binding.Input
	// ID 用户编号。
	ID int ` + "`path:\"id\" json:\"-\" validate:\"gt:0\"`" + `
}

// Output 用户资料。
type Output struct {
	Name string ` + "`json:\"name\"`" + ` // 用户姓名。
}

// Show 查询用户。
//
// 返回用户的公开资料。
//
// @Tags 用户, 后台
func Show(input Input) Output { return Output{Name: "Ada"} }
`

func newOpenAPICommandFixture(t *testing.T) (*OpenAPIGenerate, string) {
	t.Helper()
	base := t.TempDir()
	writeGeneratorTestModule(t, base)
	writeDiscoveryFixture(t, base, "app/index/api/user.go", commentedAPISource)
	command := &OpenAPIGenerate{}
	command.Configure()
	command.SetApp(&framework.App{BasePath: base})
	return command, base
}

// TestOpenAPIGenerateRunsWithoutSourceAtRuntime 验证普通注释生成后，独立编译程序在源码缺席时仍返回完整文档和真实响应。
func TestOpenAPIGenerateRunsWithoutSourceAtRuntime(t *testing.T) {
	command, base := newOpenAPICommandFixture(t)
	writeDiscoveryFixture(t, base, "cmd/verify/main.go", commentBinaryFixture)
	if err := command.Execute(console.NewInput(), generatorTestOutput()); err != nil {
		t.Fatal(err)
	}
	content := readGeneratedFile(t, base, "internal", "apidoc", "comments_generated.go")
	if !strings.Contains(content, "go:embed") || !strings.Contains(content, "go:generate") {
		t.Fatalf("生成文件未提供编译与再生成入口: %s", content)
	}
	encoded, err := os.ReadFile(filepath.Join(base, openAPICommentsJSONPath))
	if err != nil {
		t.Fatal(err)
	}
	var source openapi.SourceComments
	if err := json.Unmarshal(encoded, &source); err != nil {
		t.Fatal(err)
	}
	if got := source.Handlers["example.com/project/app/index/api.Show"]; got.Summary != "查询用户。" || len(got.Tags) != 2 || !strings.Contains(got.Description, "公开资料") {
		t.Fatalf("普通注释解析错误: %#v", got)
	}
	check := &console.Input{Options: map[string]string{"check": "true"}}
	if err := command.Execute(check, generatorTestOutput()); err != nil {
		t.Fatalf("刚生成的清单被认定为过期: %v", err)
	}
	// 可执行文件放在源码目录之外，运行时只保留二进制。
	binary := filepath.Join(t.TempDir(), "verify.exe")
	process := exec.Command("go", "build", "-mod=mod", "-trimpath", "-o", binary, "./cmd/verify")
	process.Dir = base
	process.Env = append(os.Environ(), "GOWORK=off")
	if output, err := process.CombinedOutput(); err != nil {
		t.Fatalf("独立项目编译失败: %v\n%s", err, output)
	}
	if err := os.Rename(filepath.Join(base, "app"), filepath.Join(base, "source_away")); err != nil {
		t.Fatal(err)
	}
	run := exec.Command(binary)
	run.Dir = filepath.Dir(binary)
	if output, err := run.CombinedOutput(); err != nil || !strings.Contains(string(output), "源码注释验收通过") {
		t.Fatalf("无源码运行失败: %v\n%s", err, output)
	}
}

// TestOpenAPIGenerateCheckAndFailureAreReadOnly 验证注释变化、指令拼写错误与取消不会覆盖已有产物。
func TestOpenAPIGenerateCheckAndFailureAreReadOnly(t *testing.T) {
	command, base := newOpenAPICommandFixture(t)
	check := &console.Input{Options: map[string]string{"check": "true"}}
	if err := command.Execute(check, generatorTestOutput()); err == nil {
		t.Fatal("缺少生成文件没有被检查发现")
	}
	if _, err := os.Stat(filepath.Join(base, openAPICommentsJSONPath)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("检查模式写入了文件")
	}
	if err := command.Execute(console.NewInput(), generatorTestOutput()); err != nil {
		t.Fatal(err)
	}
	previous, _ := os.ReadFile(filepath.Join(base, openAPICommentsJSONPath))
	writeDiscoveryFixture(t, base, "app/index/api/user.go", strings.Replace(commentedAPISource, "用户姓名。", "用户显示名称。", 1))
	if err := command.Execute(check, generatorTestOutput()); err == nil {
		t.Fatal("字段说明变更没有触发过期检查")
	}
	writeDiscoveryFixture(t, base, "app/index/api/user.go", strings.Replace(commentedAPISource, "@Tags", "@Tgas", 1))
	if err := command.Execute(console.NewInput(), generatorTestOutput()); err == nil || !strings.Contains(err.Error(), "user.go:") {
		t.Fatalf("非法指令缺少源码定位: %v", err)
	}
	current, _ := os.ReadFile(filepath.Join(base, openAPICommentsJSONPath))
	if string(current) != string(previous) {
		t.Fatal("失败修改了旧产物")
	}
	writeDiscoveryFixture(t, base, "app/index/api/user.go", strings.Replace(commentedAPISource, "@Tags 用户, 后台", "@ID invalid id", 1))
	if err := command.Execute(console.NewInput(), generatorTestOutput()); err == nil || !strings.Contains(err.Error(), "user.go:") {
		t.Fatalf("非法操作标识缺少源码定位: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := command.Execute(console.NewInputContext(ctx), generatorTestOutput()); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消错误丢失: %v", err)
	}
}

// TestOpenAPICommentsJoinDiscoveryTransaction 验证服务发现同步刷新已有注释清单，并在检查时发现过期内容。
func TestOpenAPICommentsJoinDiscoveryTransaction(t *testing.T) {
	command, base := newOpenAPICommandFixture(t)
	writeDiscoveryFixture(t, base, "app/index/model/user.go", crudTestModel)
	if err := command.Execute(console.NewInput(), generatorTestOutput()); err != nil {
		t.Fatal(err)
	}
	if err := RefreshControllerDiscovery(command.App); err != nil {
		t.Fatal(err)
	}
	if err := CheckControllerDiscovery(command.App); err != nil {
		t.Fatalf("发现与注释清单不一致: %v", err)
	}
	writeDiscoveryFixture(t, base, "app/index/api/user.go", strings.Replace(commentedAPISource, "公开资料", "公开资料及权限", 1))
	if err := CheckControllerDiscovery(command.App); err == nil {
		t.Fatal("服务发现检查漏掉了过期的接口说明")
	}
	if err := RefreshControllerDiscovery(command.App); err != nil {
		t.Fatal(err)
	}
	if err := command.Execute(&console.Input{Options: map[string]string{"check": "true"}}, generatorTestOutput()); err != nil {
		t.Fatal(err)
	}
}
