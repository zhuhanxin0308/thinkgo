package sqllex

import "testing"

func TestBlockCommentEndUsesDialectAndNonOverlappingDelimiters(t *testing.T) {
	const nested = "x/* a /* b */ c */tail"
	for _, dialect := range []string{"postgres", " SQLServer "} {
		if end, closed := BlockCommentEnd(nested, 1, dialect); !closed || nested[end:] != "tail" {
			t.Fatalf("%s 应消费完整嵌套注释: end=%d closed=%t", dialect, end, closed)
		}
	}
	if end, closed := BlockCommentEnd(nested, 1, "sqlite"); !closed || nested[end:] != " c */tail" {
		t.Fatalf("单层注释应在首个结束符结束: end=%d closed=%t", end, closed)
	}
	for _, text := range []string{"/*/", "/* outer /* inner */ remaining"} {
		if end, closed := BlockCommentEnd(text, 0, "sqlserver"); closed || end != len(text) {
			t.Fatalf("未闭合的注释不能重用起始符: end=%d closed=%t", end, closed)
		}
	}
	if end, closed := BlockCommentEnd("SELECT", 1, "sqlserver"); closed || end != 1 {
		t.Fatalf("错误起始位置应保持不变: end=%d closed=%t", end, closed)
	}
}
