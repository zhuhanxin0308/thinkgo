package sqllex

import "unicode/utf8"

// PostgresDollarDelimiter 识别正文中的美元引号，拒绝标识符内部的美元符号。
// PostgreSQL 标签遵循非引用标识符的起始规则，但不允许标签内部出现美元符号。
func PostgresDollarDelimiter(text string, index int) string {
	if index < 0 || index+1 >= len(text) || text[index] != '$' ||
		(index > 0 && identifierByte(text[index-1])) {
		return ""
	}
	start := index
	index++
	if text[index] == '$' {
		return "$$"
	}
	if !identifierStartByte(text[index]) {
		return ""
	}
	for index++; index < len(text); index++ {
		current := text[index]
		if current == '$' {
			return text[start : index+1]
		}
		if !identifierStartByte(current) && !(current >= '0' && current <= '9') {
			return ""
		}
	}
	return ""
}

func identifierStartByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' ||
		value == '_' || value >= utf8.RuneSelf
}
