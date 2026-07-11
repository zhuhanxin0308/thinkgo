package console

import "testing"

func newTestInput(args []string) *Input {
	return &Input{
		Args:      args,
		Options:   make(map[string]string),
		Arguments: make(map[string]string),
	}
}

// TestParseLongShortAndPositional 覆盖长选项、短选项、=写法、布尔开关与位置参数。
func TestParseLongShortAndPositional(t *testing.T) {
	in := newTestInput([]string{"User", "-p", "9000", "--force", "--name=foo"})
	in.Parse(
		[]ArgumentDefinition{{Name: "controller"}},
		[]OptionDefinition{{Name: "port", Short: "p"}, {Name: "force", Bool: true}},
	)

	if got := in.GetOption("port"); got != "9000" {
		t.Fatalf("短选项 -p 应解析为 port=9000，得到 %q", got)
	}
	if got := in.GetArgument(0); got != "User" {
		t.Fatalf("位置参数应剔除选项后为 User，得到 %q", got)
	}
	if got := in.Arguments["controller"]; got != "User" {
		t.Fatalf("命名位置参数 controller 应为 User，得到 %q", got)
	}
	if got := in.Options["force"]; got != "true" {
		t.Fatalf("布尔开关 --force 应为 true，得到 %q", got)
	}
	if got := in.GetOption("name"); got != "foo" {
		t.Fatalf("--name=foo 应解析为 foo，得到 %q", got)
	}
}

// TestParsePositionalBeforeFlag 验证选项出现在位置参数之前时仍能正确取位置参数。
func TestParsePositionalBeforeFlag(t *testing.T) {
	in := newTestInput([]string{"--force", "User"})
	in.Parse(nil, []OptionDefinition{{Name: "force", Bool: true}})

	if got := in.GetArgument(0); got != "User" {
		t.Fatalf("位置参数应为 User（不受前置选项影响），得到 %q", got)
	}
}

// TestGetOptionFallbackWithoutParse 验证未声明/未 Parse 时 GetOption 仍可即时扫描原始参数。
func TestGetOptionFallbackWithoutParse(t *testing.T) {
	in := newTestInput([]string{"-p", "9000"})
	if got := in.GetOption("p"); got != "9000" {
		t.Fatalf("未 Parse 时 GetOption 回退扫描应得到 9000，得到 %q", got)
	}
}

// TestParseAppliesDefault 验证选项默认值在未传入时生效。
func TestParseAppliesDefault(t *testing.T) {
	in := newTestInput(nil)
	in.Parse(nil, []OptionDefinition{{Name: "port", Short: "p", Default: "8080"}})
	if got := in.GetOption("port"); got != "8080" {
		t.Fatalf("未传入时应取默认值 8080，得到 %q", got)
	}
}

// TestParseKnownOptionAcceptsNegativeValue 验证声明为取值型的选项可以接收负数。
func TestParseKnownOptionAcceptsNegativeValue(t *testing.T) {
	in := newTestInput([]string{"--offset", "-1", "User"})
	in.Parse(
		[]ArgumentDefinition{{Name: "controller"}},
		[]OptionDefinition{{Name: "offset"}},
	)

	if got := in.GetOption("offset"); got != "-1" {
		t.Fatalf("取值型选项应接收负数 -1，得到 %q", got)
	}
	if got := in.GetArgument(0); got != "User" {
		t.Fatalf("负数选项值不应污染位置参数，得到 %q", got)
	}
}

// TestParseStopsOptionsAfterDoubleDash 验证 -- 后面的内容都应作为位置参数处理。
func TestParseStopsOptionsAfterDoubleDash(t *testing.T) {
	in := newTestInput([]string{"--force", "--", "--literal", "-x"})
	in.Parse(
		[]ArgumentDefinition{{Name: "first"}, {Name: "second"}},
		[]OptionDefinition{{Name: "force", Bool: true}},
	)

	if got := in.GetOption("force"); got != "true" {
		t.Fatalf("--force 应被解析为布尔选项，得到 %q", got)
	}
	if got := in.GetArgument(0); got != "--literal" {
		t.Fatalf("-- 后第一个 token 应作为位置参数，得到 %q", got)
	}
	if got := in.GetArgument(1); got != "-x" {
		t.Fatalf("-- 后第二个 token 应作为位置参数，得到 %q", got)
	}
}
