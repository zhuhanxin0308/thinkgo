package command

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework"
	"github.com/zhuhanxin0308/thinkgo/framework/console"
)

// TestListFiltersNamespaceAndSupportsRaw 验证命名空间筛选和可供脚本消费的原始列表。
func TestListFiltersNamespaceAndSupportsRaw(t *testing.T) {
	cli := console.NewConsole(nil)
	var stdout bytes.Buffer
	if err := cli.SetOutput(console.NewOutputWithWriters(&stdout, &stdout, false)); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"make:model", "make:controller", "run"} {
		if err := cli.Register(&console.Command{Signature: name, Description: "测试命令"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := cli.Register(&List{Console: cli}); err != nil {
		t.Fatal(err)
	}
	if err := cli.Run("list", "make", "--raw"); err != nil {
		t.Fatal(err)
	}
	result := stdout.String()
	if !strings.Contains(result, "make:model") || !strings.Contains(result, "make:controller") || strings.Contains(result, "run") || strings.Contains(result, "Usage:") {
		t.Fatalf("筛选后的原始列表错误: %s", result)
	}
	if strings.Index(result, "make:controller") > strings.Index(result, "make:model") {
		t.Fatalf("命令顺序不稳定: %s", result)
	}
	stdout.Reset()
	if err := cli.Run("list", "missing"); err == nil || stdout.Len() != 0 {
		t.Fatalf("未知命名空间应拒绝且不输出部分列表: %v %s", err, stdout.String())
	}
}

// TestMakeCommandAcceptsExplicitSignature 验证命令类型名与执行签名分别设置。
func TestMakeCommandAcceptsExplicitSignature(t *testing.T) {
	base := t.TempDir()
	app := framework.NewConsoleAppUninitialized(base)
	t.Cleanup(func() { _ = app.Close() })
	cli := console.NewConsole(app)
	if err := cli.Register(&MakeCommand{}); err != nil {
		t.Fatal(err)
	}
	if err := cli.Run("make:command", "Report", "report:daily"); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(base, "app", "index", "command", "report.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(content, []byte(`c.Signature = "report:daily"`)) {
		t.Fatalf("没有使用显式命令签名: %s", content)
	}
}

// TestControllerCombinedOptionsPreferAPI 保证同时指定 api 和 plain 时与 ThinkPHP 选择顺序一致。
func TestControllerCombinedOptionsPreferAPI(t *testing.T) {
	result := controllerSource("Account", "", true, true)
	if !strings.Contains(result, "func (controller *Account) Index()") || strings.Contains(result, " Create()") {
		t.Fatalf("组合选项没有优先使用 API 模板: %s", result)
	}
}
