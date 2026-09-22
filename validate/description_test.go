package validate

import "testing"

// TestDescribeRulesSharesRuntimeSyntax 验证正则中的分隔符与带引号参数不会在文档层被错误切分。
func TestDescribeRulesSharesRuntimeSyntax(t *testing.T) {
	rules, err := DescribeRules(`required|regex:/^(a|b)$/|length:1,10`)
	if err != nil || len(rules) != 3 || rules[1].Name != "regex" || rules[1].Parameter != "^(a|b)$" || len(rules[2].Arguments) != 2 {
		t.Fatalf("规则描述错误: %#v %v", rules, err)
	}
	for _, invalid := range []string{"unknown", "length:10,1", ""} {
		if _, err := DescribeRules(invalid); err == nil {
			t.Fatalf("无效规则被接受: %s", invalid)
		}
	}
}
