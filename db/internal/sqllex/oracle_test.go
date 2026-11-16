package sqllex

import (
	"strings"
	"testing"
)

// TestOracleAlternativeStringEnd 验证成对分隔符、单字符分隔符和词法前缀边界。
func TestOracleAlternativeStringEnd(t *testing.T) {
	for _, literal := range []string{"q'[a' b]'", "q'{a' b}'", "q'(a' b)'", "q'<a' b>'", "NQ'!a' b!'", "Q'界a' b界'"} {
		end, closed := OracleAlternativeStringEnd(literal+" + ?", strings.IndexByte(literal, '\''))
		if end != len(literal) || !closed {
			t.Errorf("替代引号 %q 的结束位置错误: %d %t", literal, end, closed)
		}
	}
	for _, query := range []string{"q'", "q'[unclosed", "nq' anything", "q'\xffx"} {
		end, closed := OracleAlternativeStringEnd(query, strings.IndexByte(query, '\''))
		if end != len(query) || closed {
			t.Errorf("未闭合或无效的替代引号 %q 被接受: %d %t", query, end, closed)
		}
	}
	for _, query := range []string{"'plain'", "foo#q'[a]'", "wordnq'[a]'"} {
		quote := strings.IndexByte(query, '\'')
		if end, closed := OracleAlternativeStringEnd(query, quote); end != quote || closed {
			t.Errorf("普通引号 %q 被误识别为替代引号", query)
		}
	}
}
