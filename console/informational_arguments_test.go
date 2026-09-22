package console

import (
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
		{name: "版本优先于帮助", args: []string{"run", "--help", "--version"}, want: []string{"version"}},
		{name: "保留参数分隔符后内容", args: []string{"help", "--", "--version"}, want: []string{"help", "--", "--version"}},
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
