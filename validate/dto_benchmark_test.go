package validate_test

import (
	"encoding/json"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/validate"
)

// BenchmarkValidateRulesProfile 衡量资料表单的规则映射入口，每次调用都按规则内容查找共享验证器。
func BenchmarkValidateRulesProfile(b *testing.B) {
	rules := map[string]string{"email|邮箱": "required|email", "name|姓名": "required|length:2,8"}
	check := func(data map[string]any) (validate.Result, error) { return validate.ValidateRules(data, rules) }
	benchmarkDTO(b, check,
		map[string]any{"email": "user@example.com", "name": "小陈"},
		map[string]any{"email": "invalid", "name": "陈"},
		// 两个字段都无效，默认模式只报告首个违规。
		dtoViolation{"email", "email"},
	)
}

// BenchmarkValidateCredentialFields 衡量绑定器交给验证器的凭据字段，输入已包含关联字段与默认角色。
func BenchmarkValidateCredentialFields(b *testing.B) {
	v := validate.NewValidator().SetRules(map[string]string{
		"company":  "requireIf:role,staff",
		"password": "confirm:confirmation",
	})
	check := func(data map[string]any) (validate.Result, error) {
		return v.Validate(data, validate.CollectAllErrors())
	}
	benchmarkDTO(b, check,
		map[string]any{"role": "staff", "company": "ACME", "password": "secret", "confirmation": "secret"},
		map[string]any{"role": "staff", "password": "secret", "confirmation": "other"},
		dtoViolation{"company", "requireIf"}, dtoViolation{"password", "confirm"},
	)
}

// BenchmarkValidateLineItemFields 衡量绑定器交给验证器的单个明细项字段，数量采用绑定器保留的 JSON 数值表示。
func BenchmarkValidateLineItemFields(b *testing.B) {
	v := validate.NewValidator().SetRules(map[string]string{
		"label":    "required",
		"quantity": "required|between:1,10",
	})
	check := func(data map[string]any) (validate.Result, error) {
		return v.Validate(data, validate.CollectAllErrors())
	}
	benchmarkDTO(b, check,
		map[string]any{"label": "first", "quantity": json.Number("2")},
		map[string]any{"quantity": json.Number("0")},
		dtoViolation{"label", "required"}, dtoViolation{"quantity", "between"},
	)
}

type dtoViolation struct{ field, rule string }

// benchmarkDTO 接收预先构造的字段映射，在计时前预热校验计划，每轮计入校验及结果状态读取。
// 结构化违规的副本在计时结束后核对，避免混入响应构造成本。
func benchmarkDTO(b *testing.B, check func(map[string]any) (validate.Result, error), valid, invalid map[string]any, violations ...dtoViolation) {
	for _, sample := range []struct {
		name string
		data map[string]any
		want []dtoViolation
	}{
		{"Valid", valid, nil},
		{"Invalid", invalid, violations},
	} {
		b.Run(sample.name, func(b *testing.B) {
			result, err := check(sample.data)
			if err != nil {
				b.Fatal(err)
			}
			wantValid := len(sample.want) == 0
			b.ReportAllocs()
			for b.Loop() {
				result, err = check(sample.data)
				if err != nil || result.Valid() != wantValid {
					b.Fatalf("校验结果不符: violations=%v err=%v", result.Violations(), err)
				}
			}
			got := result.Violations()
			if len(got) != len(sample.want) {
				b.Fatalf("违规数量不符: got=%v want=%v", got, sample.want)
			}
			for i, want := range sample.want {
				if got[i].Field != want.field || got[i].Rule != want.rule {
					b.Fatalf("违规字段或规则不符: got=%v want=%v", got, sample.want)
				}
			}
		})
	}
}
