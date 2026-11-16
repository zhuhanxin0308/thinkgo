package sqllex

import (
	"strings"
	"unicode/utf8"
)

// OracleAlternativeStringEnd 返回 q 或 nq 字面量的结束位置，未识别时原样返回引号索引。
func OracleAlternativeStringEnd(text string, quote int) (int, bool) {
	if quote < 1 || quote >= len(text) || text[quote] != '\'' || (text[quote-1] != 'q' && text[quote-1] != 'Q') {
		return quote, false
	}
	prefix := quote - 1
	if prefix > 0 && (text[prefix-1] == 'n' || text[prefix-1] == 'N') {
		prefix--
	}
	if prefix > 0 && (identifierByte(text[prefix-1]) || text[prefix-1] == '#') {
		return quote, false
	}
	delimiter, width := utf8.DecodeRuneInString(text[quote+1:])
	if width == 0 || delimiter == utf8.RuneError && width == 1 {
		return len(text), false
	}
	switch delimiter {
	case ' ', '\t', '\r', '\n':
		return len(text), false
	case '[':
		delimiter = ']'
	case '{':
		delimiter = '}'
	case '(':
		delimiter = ')'
	case '<':
		delimiter = '>'
	}
	start := quote + 1 + width
	endMarker := string(delimiter) + "'"
	if offset := strings.Index(text[start:], endMarker); offset >= 0 {
		return start + offset + len(endMarker), true
	}
	return len(text), false
}
