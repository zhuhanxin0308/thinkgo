// Package sqllex 保存数据库验证、重绑定与脱敏共用的词法边界。
package sqllex

import "unicode/utf8"

// PostgresStringEnd 返回单引号字符串及其合法跨行续接的结束位置。
// 遵循 standard_conforming_strings=on：只有 E 前缀及其续接启用反斜杠转义。
func PostgresStringEnd(text string, quote int) (int, bool) {
	if quote < 0 || quote >= len(text) || text[quote] != '\'' {
		return quote, false
	}
	escapes := quote > 0 && (text[quote-1] == 'E' || text[quote-1] == 'e') &&
		(quote == 1 || !identifierByte(text[quote-2]))
	for {
		end, closed := quotedEnd(text, quote, escapes)
		if !closed {
			return end, false
		}
		next, newline := continuation(text, end)
		if !newline || next >= len(text) || text[next] != '\'' {
			return end, true
		}
		quote = next
	}
}

func quotedEnd(text string, quote int, escapes bool) (int, bool) {
	for index := quote + 1; index < len(text); index++ {
		if escapes && text[index] == '\\' && index+1 < len(text) {
			index++
			continue
		}
		if text[index] == '\'' {
			if index+1 < len(text) && text[index+1] == '\'' {
				index++
				continue
			}
			return index + 1, true
		}
	}
	return len(text), false
}

// continuation 只跨越空白和完整注释；未闭合块注释留给调用方原有错误检查。
func continuation(text string, index int) (int, bool) {
	newline := false
	for index < len(text) {
		switch text[index] {
		case '\n', '\r':
			newline = true
			index++
		case ' ', '\t', '\f', '\v':
			index++
		default:
			if index+1 < len(text) && text[index:index+2] == "--" {
				index += len("--")
				for index < len(text) && text[index] != '\n' && text[index] != '\r' {
					index++
				}
				continue
			}
			if index+1 < len(text) && text[index:index+2] == "/*" {
				depth := 1
				index += len("/*")
				for index < len(text) && depth > 0 {
					if text[index] == '\n' || text[index] == '\r' {
						newline = true
					}
					if index+1 < len(text) && text[index:index+2] == "/*" {
						depth++
						index += len("/*")
					} else if index+1 < len(text) && text[index:index+2] == "*/" {
						depth--
						index += len("*/")
					} else {
						index++
					}
				}
				continue
			}
			return index, newline
		}
	}
	return index, newline
}

func identifierByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' || value == '_' || value == '$' || value >= utf8.RuneSelf
}
