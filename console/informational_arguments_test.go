package console

import (
	"bytes"
	"reflect"
	"testing"
)

// TestNormalizeInformationalArguments 验证全局帮助和版本选项在业务初始化之前解析。
func TestNormalizeInformationalArguments(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{name: "空输入", args: []string{}, want: []string{}},
		{name: "全局帮助", args: []string{"--help"}, want: []string{"help"}},
		{name: "短帮助", args: []string{"-h"}, want: []string{"help"}},
		{name: "命令后帮助", args: []string{"run", "--help"}, want: []string{"help", "run"}},
		{name: "命令前帮助", args: []string{"-h", "run"}, want: []string{"help", "run"}},
		{name: "生成器帮助不执行生成", args: []string{"make:controller", "User", "--help"}, want: []string{"help", "make:controller"}},
		{name: "长版本", args: []string{"--version"}, want: []string{"version"}},
		{name: "短版本", args: []string{"-V"}, want: []string{"version"}},
		{name: "小写短版本", args: []string{"-v"}, want: []string{"version"}},
		{name: "命令前小写版本", args: []string{"-v", "run"}, want: []string{"version"}},
		{name: "命令后小写选项保持原样", args: []string{"required", "Alice", "-v"}, want: []string{"required", "Alice", "-v"}},
		{name: "版本优先于帮助", args: []string{"run", "--help", "--version"}, want: []string{"version"}},
		{name: "保留参数分隔符后内容", args: []string{"help", "--", "--version"}, want: []string{"help", "--", "--version"}},
		{name: "保留分隔符后小写版本", args: []string{"help", "--", "-v"}, want: []string{"help", "--", "-v"}},
		{name: "普通命令不变", args: []string{"help", "run", "--raw"}, want: []string{"help", "run", "--raw"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := append([]string{}, test.args...)
			got := NormalizeInformationalArguments(test.args)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("解析结果错误: got=%v want=%v", got, test.want)
			}
			if !reflect.DeepEqual(test.args, before) {
				t.Fatalf("解析修改了调用方参数: %v", test.args)
			}
		})
	}
}

// TestCommandVerboseShortOptionSurvivesVersionAlias 验证全局版本别名不会抢占命令已有的详细输出选项。
func TestCommandVerboseShortOptionSurvivesVersionAlias(t *testing.T) {
	cli := NewConsole(nil)
	var output bytes.Buffer
	if err := cli.SetOutput(NewOutputWithWriters(&output, &output, false)); err != nil {
		t.Fatal(err)
	}
	command := &hardeningCommand{name: "required"}
	if err := cli.Register(command); err != nil {
		t.Fatal(err)
	}
	if err := cli.Run("required", "Alice", "-v"); err != nil || !command.executed {
		t.Fatalf("命令的 -v 选项未正常执行: 错误=%v 输出=%s", err, output.String())
	}
}
