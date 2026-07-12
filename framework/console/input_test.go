package console

import (
	"errors"
	"testing"
)

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
	if err := in.Parse(
		[]ArgumentDefinition{{Name: "controller"}},
		[]OptionDefinition{{Name: "port", Short: "p"}, {Name: "force", Bool: true}, {Name: "name"}},
	); err != nil {
		t.Fatalf("解析合法参数失败: %v", err)
	}

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
	if err := in.Parse([]ArgumentDefinition{{Name: "name", Required: true}}, []OptionDefinition{{Name: "force", Bool: true}}); err != nil {
		t.Fatalf("解析前置布尔选项失败: %v", err)
	}

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
	if err := in.Parse(nil, []OptionDefinition{{Name: "port", Short: "p", Default: "8080"}}); err != nil {
		t.Fatalf("应用默认值失败: %v", err)
	}
	if got := in.GetOption("port"); got != "8080" {
		t.Fatalf("未传入时应取默认值 8080，得到 %q", got)
	}
}

// TestParseKnownOptionAcceptsNegativeValue 验证声明为取值型的选项可以接收负数。
func TestParseKnownOptionAcceptsNegativeValue(t *testing.T) {
	in := newTestInput([]string{"--offset", "-1", "User"})
	if err := in.Parse(
		[]ArgumentDefinition{{Name: "controller"}},
		[]OptionDefinition{{Name: "offset"}},
	); err != nil {
		t.Fatalf("解析负数选项值失败: %v", err)
	}

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
	if err := in.Parse(
		[]ArgumentDefinition{{Name: "first"}, {Name: "second"}},
		[]OptionDefinition{{Name: "force", Bool: true}},
	); err != nil {
		t.Fatalf("解析双横线终止符失败: %v", err)
	}

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

// TestParseRejectsAmbiguousOrIncompleteInput 验证未知选项、缺少选项值、
// 重复选项、非法布尔值、缺少必填参数和多余参数都不会进入命令执行。
func TestParseRejectsAmbiguousOrIncompleteInput(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		arguments []ArgumentDefinition
		options   []OptionDefinition
	}{
		{
			name:    "未知选项",
			args:    []string{"--unknown"},
			options: []OptionDefinition{{Name: "port"}},
		},
		{
			name:    "取值选项缺值",
			args:    []string{"--port"},
			options: []OptionDefinition{{Name: "port"}},
		},
		{
			name:    "重复选项",
			args:    []string{"--port=8000", "-p", "9000"},
			options: []OptionDefinition{{Name: "port", Short: "p"}},
		},
		{
			name:    "非法布尔值",
			args:    []string{"--force=maybe"},
			options: []OptionDefinition{{Name: "force", Bool: true}},
		},
		{
			name:      "缺少必填参数",
			arguments: []ArgumentDefinition{{Name: "name", Required: true}},
		},
		{
			name:      "多余位置参数",
			args:      []string{"one", "two"},
			arguments: []ArgumentDefinition{{Name: "name", Required: true}},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			input := newTestInput(testCase.args)
			if err := input.Parse(testCase.arguments, testCase.options); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("非法输入应返回 ErrInvalidInput，实际为 %v", err)
			}
		})
	}
}

// TestParseBooleanFalseAndStateReset 验证布尔选项可显式关闭，且重复 Parse
// 不会泄漏上一次调用的选项、参数或默认值状态。
func TestParseBooleanFalseAndStateReset(t *testing.T) {
	input := newTestInput([]string{"--force=false", "first"})
	arguments := []ArgumentDefinition{{Name: "name", Required: true}}
	options := []OptionDefinition{{Name: "force", Short: "f", Bool: true, Default: "true"}}
	if err := input.Parse(arguments, options); err != nil {
		t.Fatalf("首次解析失败: %v", err)
	}
	if input.GetOption("force") != "false" || input.GetArgument(0) != "first" {
		t.Fatalf("首次解析结果错误: options=%v arguments=%v", input.Options, input.Arguments)
	}

	input.Args = []string{"second"}
	if err := input.Parse(arguments, nil); err != nil {
		t.Fatalf("再次解析失败: %v", err)
	}
	if input.GetOption("force") != "" || input.GetArgument(0) != "second" {
		t.Fatalf("再次解析泄漏旧状态: options=%v arguments=%v", input.Options, input.Arguments)
	}
}

// TestInputGettersHandleBoundsAndCompatibilityForms 验证访问器对空接收者、
// 越界位置、内联选项、布尔开关和 -- 终止符保持确定行为。
func TestInputGettersHandleBoundsAndCompatibilityForms(t *testing.T) {
	var nilInput *Input
	if nilInput.GetArgument(0) != "" || nilInput.GetOption("name") != "" {
		t.Fatal("空 Input 接收者应返回空字符串")
	}
	input := NewInput("first", "--name=value", "--force", "--", "--ignored=secret")
	if input.GetArgument(-1) != "" || input.GetArgument(99) != "" || input.GetArgument(0) != "first" {
		t.Fatalf("未解析位置参数边界错误: args=%v", input.Args)
	}
	if input.GetOption("name") != "value" || input.GetOption("force") != "true" || input.GetOption("ignored") != "" {
		t.Fatalf("兼容选项扫描错误: name=%q force=%q ignored=%q", input.GetOption("name"), input.GetOption("force"), input.GetOption("ignored"))
	}
	if err := input.Parse(
		[]ArgumentDefinition{{Name: "first"}, {Name: "literal"}},
		[]OptionDefinition{{Name: "name"}, {Name: "force", Bool: true}},
	); err != nil {
		t.Fatalf("解析输入失败: %v", err)
	}
	if input.GetArgument(99) != "" || input.GetOption("missing") != "" {
		t.Fatal("解析后越界参数和未知选项应返回空字符串")
	}
}

// TestParseRejectsInvalidDefinitions 验证命令作者无法声明重复、歧义或
// 顺序错误的参数与选项契约。
func TestParseRejectsInvalidDefinitions(t *testing.T) {
	testCases := []struct {
		name      string
		arguments []ArgumentDefinition
		options   []OptionDefinition
	}{
		{name: "invalid argument", arguments: []ArgumentDefinition{{Name: "bad name"}}},
		{name: "duplicate argument", arguments: []ArgumentDefinition{{Name: "name"}, {Name: "name"}}},
		{name: "required after optional", arguments: []ArgumentDefinition{{Name: "first"}, {Name: "second", Required: true}}},
		{name: "invalid option", options: []OptionDefinition{{Name: "bad option"}}},
		{name: "duplicate option", options: []OptionDefinition{{Name: "port"}, {Name: "port"}}},
		{name: "invalid short option", options: []OptionDefinition{{Name: "port", Short: "pp"}}},
		{name: "duplicate short option", options: []OptionDefinition{{Name: "port", Short: "p"}, {Name: "path", Short: "p"}}},
		{name: "invalid boolean default", options: []OptionDefinition{{Name: "force", Bool: true, Default: "sometimes"}}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			input := NewInput()
			if err := input.Parse(testCase.arguments, testCase.options); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("非法声明应返回 ErrInvalidInput，实际为 %v", err)
			}
			if len(input.Options) != 0 || len(input.Arguments) != 0 {
				t.Fatalf("声明校验失败后不应遗留解析状态: options=%v arguments=%v", input.Options, input.Arguments)
			}
		})
	}
}
