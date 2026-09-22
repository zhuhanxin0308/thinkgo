package console

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// TestListNamespaceAndRawOutput 验证过滤只匹配完整命名空间，原始输出不带界面标题。
func TestListNamespaceAndRawOutput(t *testing.T) {
	var output bytes.Buffer
	cli := NewConsole(nil)
	if err := cli.SetOutput(NewOutputWithWriters(&output, &output, false)); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"make:model", "make:controller", "maker:other", "version"} {
		if err := cli.Register(&hardeningCommand{name: name}); err != nil {
			t.Fatal(err)
		}
	}
	if err := cli.ShowCommandList("make", true); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if strings.Contains(text, "Usage:") || strings.Contains(text, "maker:other") || strings.Contains(text, "version") || !strings.Contains(text, "make:controller") || !strings.Contains(text, "make:model") {
		t.Fatalf("原始命名空间列表错误: %s", text)
	}
	if err := cli.ShowCommandList("unknown", false); !errors.Is(err, ErrCommandNotFound) {
		t.Fatal(err)
	}
	var missing *Console
	if err := missing.ShowCommandList("", true); !errors.Is(err, ErrInvalidCommand) {
		t.Fatal(err)
	}
	for _, name := range []string{"report:daily", "report"} {
		if err := ValidateCommandName(name); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"Report", "report\nforged", "", "report daily"} {
		if err := ValidateCommandName(name); err == nil {
			t.Fatalf("非法命令签名被接受: %q", name)
		}
	}
}
