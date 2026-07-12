package db

import (
	"fmt"
	"strings"
)

const sqlRedactionMarker = "[REDACTED]"

// redactSQLLiteralsAndComments 隐去 SQL 字符串、dollar quote 与注释内容，
// 同时保留语句结构、引用标识符和换行，便于在不泄露数据的前提下排障。
func redactSQLLiteralsAndComments(sqlText string) string {
	var result strings.Builder
	result.Grow(len(sqlText))
	for index := 0; index < len(sqlText); {
		switch sqlText[index] {
		case '\'':
			result.WriteByte('\'')
			result.WriteString(sqlRedactionMarker)
			result.WriteByte('\'')
			index++
			for index < len(sqlText) {
				if sqlText[index] == '\\' && index+1 < len(sqlText) {
					index += 2
					continue
				}
				if sqlText[index] == '\'' {
					if index+1 < len(sqlText) && sqlText[index+1] == '\'' {
						index += 2
						continue
					}
					index++
					break
				}
				index++
			}
		case '"', '`':
			closing := sqlText[index]
			result.WriteByte(sqlText[index])
			index++
			for index < len(sqlText) {
				current := sqlText[index]
				result.WriteByte(current)
				index++
				if current == '\\' && index < len(sqlText) {
					result.WriteByte(sqlText[index])
					index++
					continue
				}
				if current == closing {
					if index < len(sqlText) && sqlText[index] == closing {
						result.WriteByte(sqlText[index])
						index++
						continue
					}
					break
				}
			}
		case '[':
			result.WriteByte('[')
			index++
			for index < len(sqlText) {
				current := sqlText[index]
				result.WriteByte(current)
				index++
				if current == ']' {
					if index < len(sqlText) && sqlText[index] == ']' {
						result.WriteByte(']')
						index++
						continue
					}
					break
				}
			}
		case '-':
			if index+1 < len(sqlText) && sqlText[index+1] == '-' {
				result.WriteString("--")
				result.WriteString(sqlRedactionMarker)
				index += 2
				for index < len(sqlText) && sqlText[index] != '\n' && sqlText[index] != '\r' {
					index++
				}
				continue
			}
			result.WriteByte(sqlText[index])
			index++
		case '/':
			if index+1 < len(sqlText) && sqlText[index+1] == '*' {
				result.WriteString("/*")
				result.WriteString(sqlRedactionMarker)
				result.WriteString("*/")
				index += 2
				if closing := strings.Index(sqlText[index:], "*/"); closing >= 0 {
					index += closing + 2
				} else {
					index = len(sqlText)
				}
				continue
			}
			result.WriteByte(sqlText[index])
			index++
		case '$':
			delimiter := readDollarQuoteDelimiter(sqlText[index:])
			if delimiter == "" {
				result.WriteByte(sqlText[index])
				index++
				continue
			}
			result.WriteString(delimiter)
			result.WriteString(sqlRedactionMarker)
			index += len(delimiter)
			if closing := strings.Index(sqlText[index:], delimiter); closing >= 0 {
				result.WriteString(delimiter)
				index += closing + len(delimiter)
			} else {
				index = len(sqlText)
			}
		default:
			result.WriteByte(sqlText[index])
			index++
		}
	}
	return result.String()
}

// validateRawClause 校验显式原始条件的基本完整性和绑定参数数量。
func validateRawClause(sqlText string, argsCount int, clauseName string) error {
	if strings.TrimSpace(sqlText) == "" {
		return fmt.Errorf("%s 原始条件不能为空", clauseName)
	}
	if strings.IndexByte(sqlText, 0) >= 0 {
		return fmt.Errorf("%s 原始条件包含 NUL 字节", clauseName)
	}
	return validatePlaceholderCount(sqlText, argsCount)
}

// validateRawStatement 校验原生 SQL 入口，框架约定调用方统一使用问号占位符。
func validateRawStatement(sqlText string, argsCount int) error {
	if strings.TrimSpace(sqlText) == "" {
		return fmt.Errorf("%w: 原生 SQL 不能为空", ErrInvalidQuery)
	}
	if strings.IndexByte(sqlText, 0) >= 0 {
		return fmt.Errorf("%w: 原生 SQL 包含 NUL 字节", ErrInvalidQuery)
	}
	if err := validatePlaceholderCount(sqlText, argsCount); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidQuery, err)
	}
	return nil
}

func validatePlaceholderCount(sqlText string, argsCount int) error {
	placeholderCount, err := countSQLPlaceholders(sqlText)
	if err != nil {
		return err
	}
	if placeholderCount != argsCount {
		return fmt.Errorf("占位符数量为 %d，绑定参数数量为 %d", placeholderCount, argsCount)
	}
	return nil
}

// countSQLPlaceholders 忽略字符串、标识符引用和注释中的问号，只统计真实绑定占位符。
func countSQLPlaceholders(sqlText string) (int, error) {
	const (
		stateNormal = iota
		stateSingleQuote
		stateDoubleQuote
		stateBacktick
		stateBracket
		stateLineComment
		stateBlockComment
		stateDollarQuote
	)
	state := stateNormal
	count := 0
	dollarDelimiter := ""
	for index := 0; index < len(sqlText); index++ {
		current := sqlText[index]
		switch state {
		case stateNormal:
			switch current {
			case '?':
				// PostgreSQL 单字符 JSONB 存在操作符使用 ?? 转义；
				// ?|、?& 与 @? 语法无歧义，可直接保留且不计入绑定参数。
				if index+1 < len(sqlText) && sqlText[index+1] == '?' {
					index++
					continue
				}
				if isPostgresQuestionOperator(sqlText, index) {
					continue
				}
				count++
			case '\'':
				state = stateSingleQuote
			case '"':
				state = stateDoubleQuote
			case '`':
				state = stateBacktick
			case '[':
				state = stateBracket
			case '-':
				if index+1 < len(sqlText) && sqlText[index+1] == '-' {
					state = stateLineComment
					index++
				}
			case '/':
				if index+1 < len(sqlText) && sqlText[index+1] == '*' {
					state = stateBlockComment
					index++
				}
			case '$':
				if delimiter := readDollarQuoteDelimiter(sqlText[index:]); delimiter != "" {
					dollarDelimiter = delimiter
					state = stateDollarQuote
					index += len(delimiter) - 1
				}
			}
		case stateSingleQuote, stateDoubleQuote, stateBacktick:
			closing := byte('\'')
			if state == stateDoubleQuote {
				closing = '"'
			} else if state == stateBacktick {
				closing = '`'
			}
			if current == '\\' && index+1 < len(sqlText) {
				index++
				continue
			}
			if current == closing {
				if index+1 < len(sqlText) && sqlText[index+1] == closing {
					index++
					continue
				}
				state = stateNormal
			}
		case stateBracket:
			if current == ']' {
				if index+1 < len(sqlText) && sqlText[index+1] == ']' {
					index++
					continue
				}
				state = stateNormal
			}
		case stateLineComment:
			if current == '\n' || current == '\r' {
				state = stateNormal
			}
		case stateBlockComment:
			if current == '*' && index+1 < len(sqlText) && sqlText[index+1] == '/' {
				state = stateNormal
				index++
			}
		case stateDollarQuote:
			if strings.HasPrefix(sqlText[index:], dollarDelimiter) {
				state = stateNormal
				index += len(dollarDelimiter) - 1
				dollarDelimiter = ""
			}
		}
	}
	if state == stateLineComment {
		return count, nil
	}
	if state != stateNormal {
		return 0, fmt.Errorf("SQL 引用或注释未闭合")
	}
	return count, nil
}

// isPostgresQuestionOperator 识别没有绑定占位符歧义的 PostgreSQL 操作符。
// 独立的单字符 ? 必须由调用方写成 ??，避免根据上下文猜测而误绑参数。
func isPostgresQuestionOperator(sqlText string, index int) bool {
	if index < 0 || index >= len(sqlText) || sqlText[index] != '?' {
		return false
	}
	if index+1 < len(sqlText) && (sqlText[index+1] == '|' || sqlText[index+1] == '&') {
		return true
	}
	return index > 0 && sqlText[index-1] == '@'
}

func readDollarQuoteDelimiter(text string) string {
	if len(text) < 2 || text[0] != '$' {
		return ""
	}
	for index := 1; index < len(text); index++ {
		current := text[index]
		if current == '$' {
			return text[:index+1]
		}
		if !((current >= 'a' && current <= 'z') || (current >= 'A' && current <= 'Z') ||
			(current >= '0' && current <= '9') || current == '_') {
			return ""
		}
	}
	return ""
}
