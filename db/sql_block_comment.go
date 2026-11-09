package db

import "strings"

// skipSQLBlockComment 从已确认的 /* 起始位置扫描至完整结束边界。
// PostgreSQL 按嵌套深度处理，其他方言保留既有单层策略；未闭合时消费剩余文本。
func skipSQLBlockComment(text string, start int, dialect string) (int, bool) {
	nested := strings.EqualFold(strings.TrimSpace(dialect), "postgres")
	depth := 1
	for index := start + len("/*"); index+1 < len(text); {
		if nested && text[index] == '/' && text[index+1] == '*' {
			depth++
			index += len("/*")
			continue
		}
		if text[index] == '*' && text[index+1] == '/' {
			depth--
			index += len("*/")
			if depth == 0 {
				return index, true
			}
			continue
		}
		index++
	}
	return len(text), false
}
