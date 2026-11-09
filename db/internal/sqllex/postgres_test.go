package sqllex

import (
	"strings"
	"testing"
)

// TestPostgresStringEnd 覆盖普通/转义字符串及合法续接，不让续接间的注释问号进入正文。
func TestPostgresStringEnd(t *testing.T) {
	for _, test := range []struct {
		text   string
		suffix string
		closed bool
	}{
		{`'\', ?`, ", ?", true},
		{`E'it\'s ?', ?`, ", ?", true},
		{`e'it''s ?', ?`, ", ?", true},
		{"E'a' \t\f\v\r\n 'it\\'s ?', ?", ", ?", true},
		{"E'a' -- ?\n 'it\\'s ?', ?", ", ?", true},
		{"E'a' /* outer\n /* ? */ */ 'it\\'s ?', ?", ", ?", true},
		{"'a'\n'\\', ?", ", ?", true},
		{"E'a' /* no newline */ 'next'", " /* no newline */ 'next'", true},
		{"E'a' /* unclosed", " /* unclosed", true},
		{"'a' -- tail", " -- tail", true},
		{`'unclosed`, "", false},
		{`E'\'`, "", false},
		{"E'a'\n'unclosed", "", false},
		{`nameE'\', ?`, ", ?", true},
		{`名E'\', ?`, ", ?", true},
		{`$e'\', ?`, ", ?", true},
	} {
		quote := strings.IndexByte(test.text, '\'')
		end, closed := PostgresStringEnd(test.text, quote)
		if closed != test.closed || test.text[end:] != test.suffix {
			t.Fatalf("字符串边界错误: text=%q end=%d closed=%t", test.text, end, closed)
		}
	}
	for _, index := range []int{-1, 0, 3} {
		if _, closed := PostgresStringEnd("abc", index); closed {
			t.Fatal("非引号起始位置应拒绝")
		}
	}
}
