package sqllex

import "strings"

// BlockCommentEnd 返回 SQL 块注释结束后的字节位置。PostgreSQL 和 SQL Server 支持嵌套。
func BlockCommentEnd(text string, start int, dialect string) (int, bool) {
	if start < 0 || start > len(text)-2 || text[start:start+2] != "/*" {
		return start, false
	}
	dialect = strings.ToLower(strings.TrimSpace(dialect))
	nested := dialect == "postgres" || dialect == "sqlserver"
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
