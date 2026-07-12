package validate

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// TestRegexCacheIsBoundedAndSkipsInvalidPatterns 验证动态规则不会让全局正则缓存无限增长。
func TestRegexCacheIsBoundedAndSkipsInvalidPatterns(t *testing.T) {
	compiledRegexCache.Lock()
	compiledRegexCache.entries = make(map[string]*regexp.Regexp)
	compiledRegexCache.order = nil
	compiledRegexCache.Unlock()

	for index := 0; index < maxRegexCacheEntries+40; index++ {
		pattern := fmt.Sprintf("^value-%d$", index)
		if _, err := compileCachedRegex(pattern); err != nil {
			t.Fatalf("编译测试正则失败: %v", err)
		}
	}
	if _, err := compileCachedRegex("["); err == nil {
		t.Fatal("非法正则应返回错误")
	}
	compiledRegexCache.Lock()
	entryCount := len(compiledRegexCache.entries)
	orderCount := len(compiledRegexCache.order)
	_, invalidCached := compiledRegexCache.entries["["]
	compiledRegexCache.Unlock()
	if entryCount != maxRegexCacheEntries || orderCount != maxRegexCacheEntries {
		t.Fatalf("正则缓存应限制为 %d，实际 entries=%d order=%d", maxRegexCacheEntries, entryCount, orderCount)
	}
	if invalidCached {
		t.Fatal("非法正则不应进入缓存")
	}
}

// TestRuleParserHandlesQuotesEscapesAndLengthLimits 验证引号、转义和长度边界不会错误拆分规则。
func TestRuleParserHandlesQuotesEscapesAndLengthLimits(t *testing.T) {
	tokens, err := splitRuleSpecification(`regex:"^[^']+(a|b)\|literal$"|required`)
	if err != nil {
		t.Fatalf("拆分带引号和转义的规则失败: %v", err)
	}
	if len(tokens) != 2 || !strings.HasPrefix(tokens[0], "regex:") || tokens[1] != "required" {
		t.Fatalf("规则拆分结果错误: %#v", tokens)
	}
	if _, err := parseRuleSpecification("field", strings.Repeat("a", maxRuleSpecBytes+1)); err == nil {
		t.Fatal("超长规则列表应被拒绝")
	}
	if _, _, err := parseFieldDefinition(strings.Repeat("a", maxFieldNameBytes+1)); err == nil {
		t.Fatal("超长字段定义应被拒绝")
	}
	oversizedPattern := `regex:"` + strings.Repeat("a", maxRegexPatternBytes+1) + `"`
	if _, err := parseRuleSpecification("field", oversizedPattern); err == nil {
		t.Fatal("超长正则应被拒绝")
	}
}

// TestRuleParserRejectsMalformedParameterDelimiters 验证参数分隔符后存在尾随内容时不会被宽松接受。
func TestRuleParserRejectsMalformedParameterDelimiters(t *testing.T) {
	testCases := []string{
		`regex:"^foo$"trailing`,
		`regex:/^foo$/trailing`,
		`in:"foo,bar`,
		`eq:foo"`,
	}
	for _, specification := range testCases {
		if _, err := parseRuleSpecification("field", specification); err == nil {
			t.Fatalf("畸形规则参数 %q 应返回配置错误", specification)
		}
	}
}
