package validate

// RuleDescription 是已通过语法和参数校验的规则快照，供接口契约复用同一规则定义。
type RuleDescription struct {
	Name      string
	Parameter string
	Arguments []string
}

// DescribeRules 使用运行时验证器的解析器导出规则，避免文档使用另一套语法。
func DescribeRules(specification string) ([]RuleDescription, error) {
	rules, err := parseRuleSpecification("field", specification)
	if err != nil {
		return nil, err
	}
	result := make([]RuleDescription, 0, len(rules))
	for _, rule := range rules {
		result = append(result, RuleDescription{Name: rule.name, Parameter: rule.param, Arguments: append([]string(nil), rule.parameters...)})
	}
	return result, nil
}
