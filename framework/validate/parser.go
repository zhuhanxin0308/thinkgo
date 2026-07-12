package validate

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

const (
	maxRegexCacheEntries = 256
	maxRegexPatternBytes = 4096
	maxRuleSpecBytes     = 16384
	maxFieldNameBytes    = 512
)

var compiledRegexCache = struct {
	sync.Mutex
	entries map[string]*regexp.Regexp
	order   []string
}{entries: make(map[string]*regexp.Regexp)}

type compiledField struct {
	name  string
	alias string
	rules []compiledRule
}

type compiledRule struct {
	name       string
	param      string
	hasParam   bool
	pattern    *regexp.Regexp
	parameters []string
}

func (r compiledRule) requiresPresence() bool {
	switch r.name {
	case "required", "requireIf", "requireWith":
		return true
	default:
		return false
	}
}

func parseFieldDefinition(definition string) (string, string, error) {
	definition = strings.TrimSpace(definition)
	if definition == "" {
		return "", "", fmt.Errorf("%w: 字段名不能为空", ErrInvalidRule)
	}
	if len(definition) > maxFieldNameBytes {
		return "", "", fmt.Errorf("%w: 字段定义过长", ErrInvalidRule)
	}
	fieldName, alias, hasAlias := strings.Cut(definition, "|")
	fieldName = strings.TrimSpace(fieldName)
	alias = strings.TrimSpace(alias)
	if fieldName == "" {
		return "", "", fmt.Errorf("%w: 字段名不能为空", ErrInvalidRule)
	}
	if strings.Contains(alias, "|") {
		return "", "", fmt.Errorf("%w: 字段 %q 包含多个别名分隔符", ErrInvalidRule, fieldName)
	}
	if hasAlias && alias == "" {
		return "", "", fmt.Errorf("%w: 字段 %q 的别名不能为空", ErrInvalidRule, fieldName)
	}
	return fieldName, alias, nil
}

func parseRuleSpecification(field string, specification string) ([]compiledRule, error) {
	if len(specification) > maxRuleSpecBytes {
		return nil, fmt.Errorf("%w: 字段 %q 的规则列表过长", ErrInvalidRule, field)
	}
	tokens, err := splitRuleSpecification(specification)
	if err != nil {
		return nil, fmt.Errorf("%w: 字段 %q: %v", ErrInvalidRule, field, err)
	}
	compiled := make([]compiledRule, 0, len(tokens))
	seenRules := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		rule, err := parseRuleToken(token)
		if err != nil {
			return nil, fmt.Errorf("字段 %q: %w", field, err)
		}
		signature := rule.name + "\x00" + rule.param
		if _, duplicate := seenRules[signature]; duplicate {
			return nil, fmt.Errorf("%w: 字段 %q 重复规则 %q", ErrInvalidRule, field, rule.name)
		}
		seenRules[signature] = struct{}{}
		compiled = append(compiled, rule)
	}
	return compiled, nil
}

func splitRuleSpecification(specification string) ([]string, error) {
	if strings.TrimSpace(specification) == "" {
		return nil, fmt.Errorf("规则列表不能为空")
	}
	var tokens []string
	var current strings.Builder
	var quote rune
	regexSlashDelimited := false
	parameterClosed := false
	escaped := false
	for _, character := range specification {
		if parameterClosed && character != '|' {
			if unicode.IsSpace(character) {
				current.WriteRune(character)
				continue
			}
			return nil, fmt.Errorf("参数结束分隔符后包含尾随内容")
		}
		if escaped {
			current.WriteRune(character)
			escaped = false
			continue
		}
		if character == '\\' {
			current.WriteRune(character)
			escaped = true
			continue
		}
		if quote != 0 {
			current.WriteRune(character)
			if character == quote {
				quote = 0
				parameterClosed = true
			}
			continue
		}
		if regexSlashDelimited {
			current.WriteRune(character)
			if character == '/' {
				regexSlashDelimited = false
				parameterClosed = true
			}
			continue
		}
		if character == '/' && strings.TrimSpace(current.String()) == "regex:" {
			regexSlashDelimited = true
			current.WriteRune(character)
			continue
		}
		if (character == '\'' || character == '"') && canStartQuotedParameter(current.String()) {
			quote = character
			current.WriteRune(character)
			continue
		}
		if character == '|' {
			token := strings.TrimSpace(current.String())
			if token == "" {
				return nil, fmt.Errorf("规则列表包含空规则")
			}
			tokens = append(tokens, token)
			current.Reset()
			parameterClosed = false
			continue
		}
		current.WriteRune(character)
	}
	if quote != 0 {
		return nil, fmt.Errorf("规则参数引号未闭合")
	}
	if regexSlashDelimited {
		return nil, fmt.Errorf("斜杠分隔的正则表达式未闭合")
	}
	if escaped {
		return nil, fmt.Errorf("规则以未完成的转义符结尾")
	}
	last := strings.TrimSpace(current.String())
	if last == "" {
		return nil, fmt.Errorf("规则列表包含空规则")
	}
	tokens = append(tokens, last)
	return tokens, nil
}

func canStartQuotedParameter(current string) bool {
	_, parameter, hasParameter := strings.Cut(current, ":")
	return hasParameter && strings.TrimSpace(parameter) == ""
}

func parseRuleToken(token string) (compiledRule, error) {
	ruleName, rawParam, hasParam := strings.Cut(token, ":")
	ruleName = strings.TrimSpace(ruleName)
	if ruleName == "" {
		return compiledRule{}, fmt.Errorf("%w: 规则名称不能为空", ErrInvalidRule)
	}
	if !isKnownRule(ruleName) {
		return compiledRule{}, fmt.Errorf("%w: %q", ErrUnknownRule, ruleName)
	}
	param, err := normalizeRuleParam(rawParam, hasParam)
	if err != nil {
		return compiledRule{}, fmt.Errorf("%w: %s: %v", ErrInvalidRule, ruleName, err)
	}
	rule := compiledRule{name: ruleName, param: param, hasParam: hasParam}
	if err := validateCompiledRule(&rule); err != nil {
		return compiledRule{}, fmt.Errorf("%w: %s: %v", ErrInvalidRule, ruleName, err)
	}
	return rule, nil
}

func normalizeRuleParam(raw string, hasParam bool) (string, error) {
	if !hasParam {
		return "", nil
	}
	param := strings.TrimSpace(raw)
	if param != "" && (param[0] == '"' || param[0] == '\'') {
		quote := param[0]
		if len(param) < 2 || param[len(param)-1] != quote {
			return "", fmt.Errorf("规则参数引号未闭合")
		}
		return unescapeQuotedParameter(param[1:len(param)-1], quote), nil
	}
	if param != "" && (param[len(param)-1] == '"' || param[len(param)-1] == '\'') {
		return "", fmt.Errorf("规则参数包含未配对引号")
	}
	return param, nil
}

func unescapeQuotedParameter(value string, quote byte) string {
	var unescaped strings.Builder
	for index := 0; index < len(value); index++ {
		if value[index] == '\\' && index+1 < len(value) && value[index+1] == quote {
			unescaped.WriteByte(quote)
			index++
			continue
		}
		unescaped.WriteByte(value[index])
	}
	return unescaped.String()
}

func validateCompiledRule(rule *compiledRule) error {
	if isNoParameterRule(rule.name) {
		if rule.hasParam {
			return fmt.Errorf("该规则不接受参数")
		}
		return nil
	}

	switch rule.name {
	case "confirm":
		// confirm 允许省略目标字段，此时使用 field_confirm。
		return nil
	case "eq":
		if !rule.hasParam {
			return fmt.Errorf("缺少比较值")
		}
		return nil
	case "in", "notIn":
		if !rule.hasParam || rule.param == "" {
			return fmt.Errorf("候选值不能为空")
		}
		rule.parameters = splitAndTrim(rule.param)
		if containsEmpty(rule.parameters) {
			return fmt.Errorf("候选值不能包含空项")
		}
		return nil
	case "between", "notBetween":
		rule.parameters = splitAndTrim(rule.param)
		if !rule.hasParam || len(rule.parameters) != 2 || containsEmpty(rule.parameters) {
			return fmt.Errorf("必须提供两个数值边界")
		}
		minimum, ok := parseNumericString(rule.parameters[0])
		if !ok {
			return fmt.Errorf("下界不是有效数值")
		}
		maximum, ok := parseNumericString(rule.parameters[1])
		if !ok || minimum.Cmp(maximum) > 0 {
			return fmt.Errorf("上界无效或小于下界")
		}
		return nil
	case "length":
		rule.parameters = splitAndTrim(rule.param)
		if !rule.hasParam || (len(rule.parameters) != 1 && len(rule.parameters) != 2) || containsEmpty(rule.parameters) {
			return fmt.Errorf("必须提供一个长度或两个长度边界")
		}
		minimum, err := parseNonNegativeInt(rule.parameters[0])
		if err != nil {
			return err
		}
		if len(rule.parameters) == 2 {
			maximum, err := parseNonNegativeInt(rule.parameters[1])
			if err != nil || minimum > maximum {
				return fmt.Errorf("长度上界无效或小于下界")
			}
		}
		return nil
	case "max", "min", "gt", "lt", "egt", "elt":
		if !rule.hasParam || rule.param == "" {
			return fmt.Errorf("缺少数值边界")
		}
		if _, ok := parseNumericString(rule.param); !ok {
			return fmt.Errorf("边界不是有效数值")
		}
		return nil
	case "regex":
		if !rule.hasParam || rule.param == "" {
			return fmt.Errorf("正则表达式不能为空")
		}
		pattern := rule.param
		if len(pattern) >= 2 && strings.HasPrefix(pattern, "/") && strings.HasSuffix(pattern, "/") {
			pattern = pattern[1 : len(pattern)-1]
		}
		if len(pattern) > maxRegexPatternBytes {
			return fmt.Errorf("正则表达式超过 %d 字节", maxRegexPatternBytes)
		}
		compiled, err := compileCachedRegex(pattern)
		if err != nil {
			return fmt.Errorf("正则表达式无效: %w", err)
		}
		rule.param = pattern
		rule.pattern = compiled
		return nil
	case "different", "dateFormat", "after", "before":
		if !rule.hasParam || rule.param == "" {
			return fmt.Errorf("目标字段、日期或格式不能为空")
		}
		return nil
	case "requireIf":
		rule.parameters = splitAndTrim(rule.param)
		if !rule.hasParam || len(rule.parameters) != 2 || containsEmpty(rule.parameters) {
			return fmt.Errorf("必须提供关联字段和期望值")
		}
		return nil
	case "requireWith":
		rule.parameters = splitAndTrim(rule.param)
		if !rule.hasParam || len(rule.parameters) == 0 || containsEmpty(rule.parameters) {
			return fmt.Errorf("至少提供一个关联字段")
		}
		return nil
	default:
		return fmt.Errorf("未实现规则参数校验")
	}
}

func isNoParameterRule(name string) bool {
	switch name {
	case "required", "number", "integer", "float", "boolean", "email", "array", "accepted",
		"date", "alpha", "alphaNum", "alphaDash", "chs", "chsAlpha", "chsAlphaNum", "chsDash",
		"ip", "url", "mobile", "idCard":
		return true
	default:
		return false
	}
}

func isKnownRule(ruleName string) bool {
	switch ruleName {
	case "required", "number", "integer", "float", "boolean", "email", "array", "accepted",
		"date", "alpha", "alphaNum", "alphaDash", "chs", "chsAlpha", "chsAlphaNum", "chsDash",
		"ip", "url", "in", "notIn", "between", "notBetween", "length", "max", "min", "eq",
		"gt", "lt", "egt", "elt", "regex", "confirm", "different", "mobile", "dateFormat",
		"after", "before", "requireIf", "requireWith", "idCard":
		return true
	default:
		return false
	}
}

func compileCachedRegex(pattern string) (*regexp.Regexp, error) {
	compiledRegexCache.Lock()
	if compiled, exists := compiledRegexCache.entries[pattern]; exists {
		compiledRegexCache.Unlock()
		return compiled, nil
	}
	compiledRegexCache.Unlock()

	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	compiledRegexCache.Lock()
	defer compiledRegexCache.Unlock()
	if existing, exists := compiledRegexCache.entries[pattern]; exists {
		return existing, nil
	}
	if len(compiledRegexCache.order) >= maxRegexCacheEntries {
		oldest := compiledRegexCache.order[0]
		compiledRegexCache.order = compiledRegexCache.order[1:]
		delete(compiledRegexCache.entries, oldest)
	}
	compiledRegexCache.entries[pattern] = compiled
	compiledRegexCache.order = append(compiledRegexCache.order, pattern)
	return compiled, nil
}

func splitAndTrim(value string) []string {
	parts := strings.Split(value, ",")
	for index := range parts {
		parts[index] = strings.TrimSpace(parts[index])
	}
	return parts
}

func containsEmpty(values []string) bool {
	for _, value := range values {
		if value == "" {
			return true
		}
	}
	return false
}

func parseNonNegativeInt(value string) (int, error) {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("长度必须是非负整数")
	}
	return parsed, nil
}
