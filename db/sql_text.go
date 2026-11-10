package db

import (
	"fmt"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/v3/db/internal/sqllex"
)

const sqlRedactionMarker = "[REDACTED]"

// redactSQLLiteralsAndComments 隐去 SQL 字符串、dollar quote 与注释内容。
// 只有当前方言明确支持的标识符引用才会原样保留；未知方言采用保守脱敏。
func redactSQLLiteralsAndComments(sqlText string, dialect string) string {
	dialect = strings.ToLower(strings.TrimSpace(dialect))
	backslashEscapes := sqlBackslashEscapes(dialect)
	var result strings.Builder
	result.Grow(len(sqlText))
	for index := 0; index < len(sqlText); {
		switch sqlText[index] {
		case '\'':
			if dialect == "postgres" {
				end, closed := sqllex.PostgresStringEnd(sqlText, index)
				result.WriteByte('\'')
				result.WriteString(sqlRedactionMarker)
				if closed {
					result.WriteByte('\'')
				}
				index = end
				continue
			}
			index = redactSQLQuotedToken(&result, sqlText, index, '\'', backslashEscapes)
		case '"':
			if doubleQuoteIsIdentifier(dialect) {
				index = copySQLQuotedToken(&result, sqlText, index, '"', backslashEscapes)
			} else {
				index = redactSQLQuotedToken(&result, sqlText, index, '"', backslashEscapes)
			}
		case '`':
			if dialect == "mysql" {
				index = copySQLQuotedToken(&result, sqlText, index, '`', backslashEscapes)
			} else {
				index = redactSQLQuotedToken(&result, sqlText, index, '`', backslashEscapes)
			}
		case '[':
			if dialect == "postgres" {
				// PostgreSQL 方括号是数组/下标语法，内部字符串必须继续走各自的词法扫描。
				result.WriteByte('[')
				index++
			} else if dialect == "sqlserver" {
				index = copySQLQuotedToken(&result, sqlText, index, ']', backslashEscapes)
			} else {
				index = redactSQLQuotedToken(&result, sqlText, index, ']', backslashEscapes)
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
				index, _ = skipSQLBlockComment(sqlText, index, dialect)
				continue
			}
			result.WriteByte(sqlText[index])
			index++
		case '$':
			delimiter := readDollarQuoteDelimiter(sqlText[index:])
			if dialect == "postgres" {
				delimiter = sqllex.PostgresDollarDelimiter(sqlText, index)
			}
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

func doubleQuoteIsIdentifier(dialect string) bool {
	switch dialect {
	case "postgres", "sqlserver", "oracle":
		return true
	default:
		return false
	}
}

func redactSQLQuotedToken(result *strings.Builder, sqlText string, index int, closing byte, backslashEscapes bool) int {
	result.WriteByte(sqlText[index])
	result.WriteString(sqlRedactionMarker)
	index++
	for index < len(sqlText) {
		current := sqlText[index]
		if backslashEscapes && current == '\\' && index+1 < len(sqlText) {
			index += 2
			continue
		}
		if current == closing {
			if index+1 < len(sqlText) && sqlText[index+1] == closing {
				index += 2
				continue
			}
			result.WriteByte(closing)
			return index + 1
		}
		index++
	}
	return index
}

func copySQLQuotedToken(result *strings.Builder, sqlText string, index int, closing byte, backslashEscapes bool) int {
	result.WriteByte(sqlText[index])
	index++
	for index < len(sqlText) {
		current := sqlText[index]
		result.WriteByte(current)
		index++
		if backslashEscapes && current == '\\' && index < len(sqlText) {
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
	return index
}

// SQLite 与 PostgreSQL 普通引用不使用反斜杠转义；PostgreSQL E 字符串由共享扫描器处理。
func sqlBackslashEscapes(dialects ...string) bool {
	if len(dialects) == 0 {
		return true
	}
	dialect := strings.ToLower(strings.TrimSpace(dialects[0]))
	return dialect != "sqlite" && dialect != "postgres"
}

func sqlBuilderDialect(builder Builder) string {
	if isNilDatabaseDependency(builder) {
		return ""
	}
	return builder.DialectName()
}

// validateRawClause 校验显式原始条件的基本完整性和绑定参数数量。
func validateRawClause(sqlText string, argsCount int, clauseName string, dialects ...string) error {
	if strings.TrimSpace(sqlText) == "" {
		return fmt.Errorf("%s 原始条件不能为空", clauseName)
	}
	if strings.IndexByte(sqlText, 0) >= 0 {
		return fmt.Errorf("%s 原始条件包含 NUL 字节", clauseName)
	}
	return validatePlaceholderCount(sqlText, argsCount, dialects...)
}

// validateRawStatement 校验原生 SQL 入口，框架约定调用方统一使用问号占位符。
func validateRawStatement(sqlText string, argsCount int, dialects ...string) error {
	if strings.TrimSpace(sqlText) == "" {
		return fmt.Errorf("%w: 原生 SQL 不能为空", ErrInvalidQuery)
	}
	if strings.IndexByte(sqlText, 0) >= 0 {
		return fmt.Errorf("%w: 原生 SQL 包含 NUL 字节", ErrInvalidQuery)
	}
	if err := validatePlaceholderCount(sqlText, argsCount, dialects...); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidQuery, err)
	}
	return nil
}

func validatePlaceholderCount(sqlText string, argsCount int, dialects ...string) error {
	placeholderCount, err := countSQLPlaceholders(sqlText, dialects...)
	if err != nil {
		return err
	}
	if placeholderCount != argsCount {
		return fmt.Errorf("占位符数量为 %d，绑定参数数量为 %d", placeholderCount, argsCount)
	}
	return nil
}

// countSQLPlaceholders 忽略字符串、标识符引用和注释中的问号，只统计真实绑定占位符。
func countSQLPlaceholders(sqlText string, dialects ...string) (int, error) {
	backslashEscapes := sqlBackslashEscapes(dialects...)
	dialect := ""
	if len(dialects) > 0 {
		dialect = dialects[0]
	}
	postgres := strings.EqualFold(strings.TrimSpace(dialect), "postgres")
	const (
		stateNormal = iota
		stateSingleQuote
		stateDoubleQuote
		stateBacktick
		stateBracket
		stateLineComment
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
				if postgres {
					end, closed := sqllex.PostgresStringEnd(sqlText, index)
					if !closed {
						return 0, fmt.Errorf("SQL 引用或注释未闭合")
					}
					index = end - 1
					continue
				}
				state = stateSingleQuote
			case '"':
				state = stateDoubleQuote
			case '`':
				state = stateBacktick
			case '[':
				// PostgreSQL 的数组构造、下标和切片仍是 SQL 正文，与 Rebind 保持一致。
				// 其他方言继续保留方括号标识符的引用规则。
				if !postgres {
					state = stateBracket
				}
			case '-':
				if index+1 < len(sqlText) && sqlText[index+1] == '-' {
					state = stateLineComment
					index++
				}
			case '/':
				if index+1 < len(sqlText) && sqlText[index+1] == '*' {
					next, closed := skipSQLBlockComment(sqlText, index, dialect)
					if !closed {
						return 0, fmt.Errorf("SQL 引用或注释未闭合")
					}
					index = next - 1
				}
			case '$':
				delimiter := readDollarQuoteDelimiter(sqlText[index:])
				if postgres {
					delimiter = sqllex.PostgresDollarDelimiter(sqlText, index)
				}
				if delimiter != "" {
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
			if backslashEscapes && current == '\\' && index+1 < len(sqlText) {
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
