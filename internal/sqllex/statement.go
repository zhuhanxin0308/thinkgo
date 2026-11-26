package sqllex

import "strings"

// MigrationStatementCommand 返回一条可识别的 MySQL 或 Oracle 语句的主命令。
// 不确定的词法、可执行注释和叠加语句返回空，供迁移选择保守的执行边界。
func MigrationStatementCommand(text, dialect string) string {
	mysql := dialect == "mysql"
	if (!mysql && dialect != "oracle") || strings.IndexByte(text, 0) >= 0 {
		return ""
	}
	var command string
	depth := 0
	terminated, cteBodyClosed := false, false
	for index := 0; index < len(text); {
		current := text[index]
		if sqlStatementSpace(current) {
			index++
			continue
		}
		if current == '#' && mysql || strings.HasPrefix(text[index:], "--") &&
			(!mysql || index+2 == len(text) || text[index+2] <= ' ' || text[index+2] == 0x7f) {
			// MySQL 行注释只在 LF 或输入结束时终止；保留 Oracle 的 CR/LF 边界。
			var lineEnd int
			if mysql {
				lineEnd = strings.IndexByte(text[index:], '\n')
			} else {
				lineEnd = strings.IndexAny(text[index:], "\r\n")
			}
			if lineEnd < 0 {
				break
			}
			index += lineEnd
			continue
		}
		if strings.HasPrefix(text[index:], "/*") {
			end, closed := BlockCommentEnd(text, index, dialect)
			if !closed || mysql && executableMySQLComment(text[index+2:end-2]) {
				return ""
			}
			index = end
			continue
		}
		if terminated || command == "" && !identifierByte(current) {
			return ""
		}
		if identifierByte(current) {
			end := index + 1
			for end < len(text) && identifierByte(text[end]) {
				end++
			}
			word := text[index:end]
			if command == "" || mysql && command == "WITH" && depth == 0 && cteBodyClosed && mysqlWithCommand(word) {
				command = strings.ToUpper(word)
			} else if mysql && (command == "SELECT" && (strings.EqualFold(word, "OUTFILE") || strings.EqualFold(word, "DUMPFILE")) ||
				command == "INSERT" && strings.EqualFold(word, "DELAYED")) {
				return ""
			}
			index = end
			continue
		}
		switch current {
		case '\'', '"', '`':
			end, closed := migrationQuotedEnd(text, index, mysql)
			if !closed {
				return ""
			}
			index = end
			continue
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return ""
			}
			cteBodyClosed = cteBodyClosed || depth == 0
		case ';':
			terminated = true
		}
		index++
	}
	if depth != 0 {
		return ""
	}
	return command
}

func executableMySQLComment(body string) bool {
	return strings.HasPrefix(body, "!") || len(body) >= 2 && strings.EqualFold(body[:2], "M!")
}

func mysqlWithCommand(word string) bool {
	switch strings.ToUpper(word) {
	case "SELECT", "UPDATE", "DELETE":
		return true
	}
	return false
}

func migrationQuotedEnd(text string, start int, mysql bool) (int, bool) {
	if !mysql {
		if text[start] == '`' {
			return start, false
		}
		if end, closed := OracleAlternativeStringEnd(text, start); end != start {
			return end, closed
		}
		return migrationStandardQuotedEnd(text, start, false)
	}
	plain, plainClosed := migrationStandardQuotedEnd(text, start, false)
	escaped, escapedClosed := migrationStandardQuotedEnd(text, start, true)
	// MySQL 会话可能启用 NO_BACKSLASH_ESCAPES，只在两种模式边界一致时自动使用事务。
	return plain, plainClosed && escapedClosed && plain == escaped
}

func migrationStandardQuotedEnd(text string, start int, backslash bool) (int, bool) {
	closing := text[start]
	for index := start + 1; index < len(text); index++ {
		if backslash && text[index] == '\\' {
			index++
			continue
		}
		if text[index] != closing {
			continue
		}
		if index+1 < len(text) && text[index+1] == closing {
			index++
			continue
		}
		return index + 1, true
	}
	return len(text), false
}

func sqlStatementSpace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n' || value == '\v' || value == '\f'
}
