package deploy

import (
	"context"
	"testing"

	framework "github.com/zhuhanxin0308/thinkgo/v3"
)

type uncheckedLevelCheck struct {
	name  string
	level Level
}

func (check uncheckedLevelCheck) Name() string { return check.name }
func (check uncheckedLevelCheck) Run(context.Context, *framework.App) Result {
	return Result{Name: "伪造名称", Level: check.level, Message: "原始消息"}
}

// TestCustomCheckLevelsFailClosed 覆盖直接扩展、工厂包装和手工报告的统一门禁语义。
func TestCustomCheckLevelsFailClosed(t *testing.T) {
	for _, level := range []Level{"", "UNKNOWN", "pass", LevelPass, LevelWarn, LevelFail} {
		t.Run(string(level), func(t *testing.T) {
			custom := uncheckedLevelCheck{name: "a.custom", level: level}
			wrapped, err := NewCheck("b.wrapped", custom.Run)
			if err != nil {
				t.Fatal(err)
			}
			last, err := NewCheck("z.last", func(context.Context, *framework.App) Result { return pass("继续执行") })
			if err != nil {
				t.Fatal(err)
			}
			auditor, err := NewAuditor(last, wrapped, custom)
			if err != nil {
				t.Fatal(err)
			}
			report := auditor.Run(context.Background(), nil)
			if len(report.Results) != 3 || report.Results[2].Name != "z.last" || report.Results[2].Level != LevelPass {
				t.Fatalf("非法状态阻止了后续检查: %#v", report)
			}
			validLevel := level == LevelPass || level == LevelWarn || level == LevelFail
			for index, name := range []string{"a.custom", "b.wrapped"} {
				result := report.Results[index]
				if result.Name != name || !validLevel && (result.Level != LevelFail || result.Message != "检查器返回了非法状态") {
					t.Fatalf("检查身份或状态未统一归一化: %#v", result)
				}
				if validLevel && (result.Level != level || result.Message != "原始消息") {
					t.Fatalf("合法检查结果被修改: %#v", result)
				}
			}
			for _, strict := range []bool{false, true} {
				wantFailed := level != LevelPass && (strict || level != LevelWarn)
				for _, candidate := range []Report{report, {Results: []Result{{Level: level}}}} {
					if got := candidate.Failed(strict); got != wantFailed {
						t.Fatalf("状态 %q 在严格模式 %t 阻断=%t，预期=%t", level, strict, got, wantFailed)
					}
				}
			}
		})
	}
}
