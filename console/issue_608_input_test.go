package console

import (
	"errors"
	"reflect"
	"testing"
)

type reservedOptionTestCommand struct {
	Command
	optionName  string
	optionShort string
}

func (c *reservedOptionTestCommand) Configure() {
	c.Signature = "custom:reserved"
	c.Description = "测试保留全局选项拦截"
	c.AddOption(c.optionName, c.optionShort, "冲突选项", "")
}

func (c *reservedOptionTestCommand) Execute(*Input, *Output) error { return nil }

func TestInputParseRejectsNonFiniteNegativeTokensAndAcceptsFiniteNumbers(t *testing.T) {
	options := []OptionDefinition{{Name: "path", Short: "p"}}

	for _, badToken := range []string{"-inf", "-INF", "-infinity", "-Infinity", "-nan", "-NaN", "-1e999"} {
		input := NewInput("--path", badToken)
		if err := input.Parse(nil, options); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("非有限词元 %q 不得被当作负数选项值吞入: err=%v options=%#v", badToken, err, input.Options)
		}
	}

	for _, validNumber := range []string{"-1", "-0", "-3.14", "-.5", "-2.5e3"} {
		input := NewInput("--path", validNumber)
		if err := input.Parse(nil, options); err != nil {
			t.Fatalf("合法有限负数 %q 应允许作为选项值: %v", validNumber, err)
		}
		if got := input.GetOption("path"); got != validNumber {
			t.Fatalf("负数选项值被改写: got=%q want=%q", got, validNumber)
		}
	}
}

func TestNormalizeInformationalArgumentsSkipsPreCommandOptionValues(t *testing.T) {
	cases := []struct {
		args []string
		want []string
	}{
		{args: []string{"-p", "8000", "run", "--help"}, want: []string{"help", "run"}},
		{args: []string{"--port", "8000", "run", "-h"}, want: []string{"help", "run"}},
		{args: []string{"--app", "admin", "route:list", "--help"}, want: []string{"help", "route:list"}},
		{args: []string{"-p", "8000", "--help"}, want: []string{"help"}},
		{args: []string{"--help", "run"}, want: []string{"help", "run"}},
		{args: []string{"run", "-v", "--help"}, want: []string{"help", "run"}},
		{args: []string{"-p", "8000", "run", "--version"}, want: []string{"version"}},
	}
	for _, tc := range cases {
		if got := NormalizeInformationalArguments(tc.args); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("NormalizeInformationalArguments(%#v) = %#v, want %#v", tc.args, got, tc.want)
		}
	}
}

func TestConsoleRegisterRejectsReservedGlobalFlags(t *testing.T) {
	for _, reserved := range []struct {
		name  string
		short string
	}{
		{name: "help", short: ""},
		{name: "version", short: ""},
		{name: "host", short: "h"},
		{name: "variant", short: "V"},
	} {
		cli := NewConsole(nil)
		err := cli.Register(&reservedOptionTestCommand{optionName: reserved.name, optionShort: reserved.short})
		if !errors.Is(err, ErrInvalidCommand) {
			t.Fatalf("占用保留全局标志 (--%s / -%s) 应被拒绝: %v", reserved.name, reserved.short, err)
		}
	}
}
