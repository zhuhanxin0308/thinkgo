package db

import (
	"fmt"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/v3/internal/sqllex"
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
			if dialect == "oracle" {
				if end, closed := sqllex.OracleAlternativeStringEnd(sqlText, index); end != index {
					result.WriteString("'[" + sqlRedactionMarker + "]")
					if closed {
						result.WriteByte('\'')
					}
					index = end
					continue
				}
			}
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
				index = copySQLQuotedToken(&result, sqlText, index, '"', false)
			} else {
				index = redactSQLQuotedToken(&result, sqlText, index, '"', backslashEscapes)
			}
		case '`':
			if dialect == "postgres" {
				result.WriteByte('`')
				index++
			} else if dialect == "mysql" {
				index = copySQLQuotedToken(&result, sqlText, index, '`', false)
			} else {
				index = redactSQLQuotedToken(&result, sqlText, index, '`', false)
			}
		case '[':
			if dialect == "postgres" {
				// PostgreSQL 方括号是数组/下标语法，内部字符串必须继续走各自的词法扫描。
				result.WriteByte('[')
				index++
			} else if dialect == "sqlserver" {
				index = copySQLQuotedToken(&result, sqlText, index, ']', false)
			} else {
				index = redactSQLQuotedToken(&result, sqlText, index, ']', false)
			}
		case '-':
			if index+1 < len(sqlText) && sqlText[index+1] == '-' &&
				(dialect != "mysql" || index+2 == len(sqlText) || sqlText[index+2] <= ' ' || sqlText[index+2] == 0x7f) {
				result.WriteString("--")
				result.WriteString(sqlRedactionMarker)
				index += 2
				for index < len(sqlText) && sqlText[index] != '\n' && (sqlText[index] != '\r' || dialect == "sqlite" || dialect == "mysql") {
					index++
				}
				continue
			}
			result.WriteByte(sqlText[index])
			index++
		case '#':
			if dialect == "mysql" {
				result.WriteByte('#')
				result.WriteString(sqlRedactionMarker)
				index++
				for index < len(sqlText) && sqlText[index] != '\n' {
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
			var delimiter string
			if dialect == "postgres" || dialect == "" {
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

// SQLite、SQL Server、Oracle 和 PostgreSQL 的普通引用通过重复分隔符转义。
// PostgreSQL 的 E 字符串单独处理；未声明方言时仍保留兼容的反斜杠处理。
func sqlBackslashEscapes(dialects ...string) bool {
	if len(dialects) == 0 {
		return true
	}
	dialect := strings.ToLower(strings.TrimSpace(dialects[0]))
	switch dialect {
	case "sqlite", "postgres", "sqlserver", "oracle":
		return false
	default:
		return true
	}
}

func sqlBuilderDialect(builder Builder) string {
	if isNilDatabaseDependency(builder) {
		return ""
	}
	return builder.DialectName()
}

// validateRawClause 校验显式原始条件的完整性、参数数量和语句边界。
func validateRawClause(sqlText string, argsCount int, clauseName string, dialects ...string) error {
	if strings.TrimSpace(sqlText) == "" {
		return fmt.Errorf("%s 原始条件不能为空", clauseName)
	}
	if strings.IndexByte(sqlText, 0) >= 0 {
		return fmt.Errorf("%s 原始条件包含 NUL 字节", clauseName)
	}
	return validateSQLPlaceholders(sqlText, argsCount, true, dialects...)
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
	return validateSQLPlaceholders(sqlText, argsCount, false, dialects...)
}

func validateSQLPlaceholders(sqlText string, argsCount int, rejectSeparators bool, dialects ...string) error {
	placeholderCount, err := scanSQLPlaceholders(sqlText, rejectSeparators, dialects...)
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
	return scanSQLPlaceholders(sqlText, false, dialects...)
}

func scanSQLPlaceholders(sqlText string, rejectSeparators bool, dialects ...string) (int, error) {
	backslashEscapes := sqlBackslashEscapes(dialects...)
	dialect := ""
	if len(dialects) > 0 {
		dialect = strings.ToLower(strings.TrimSpace(dialects[0]))
	}
	postgres := dialect == "postgres"
	mysql := dialect == "mysql"
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
			case ';':
				if rejectSeparators {
					return 0, fmt.Errorf("原始条件不能包含 SQL 语句分隔符")
				}
			case '?':
				// PostgreSQL 单字符 JSONB 存在操作符使用 ?? 转义；
				// ?|、?& 与 @? 语法无歧义，可直接保留且不计入绑定参数。
				if postgres {
					if index+1 < len(sqlText) && sqlText[index+1] == '?' {
						index++
						continue
					}
					if sqllex.PostgresQuestionOperator(sqlText, index) {
						continue
					}
				}
				count++
			case '\'':
				if dialect == "oracle" {
					if end, closed := sqllex.OracleAlternativeStringEnd(sqlText, index); end != index {
						if !closed {
							return 0, fmt.Errorf("SQL 引用或注释未闭合")
						}
						index = end - 1
						continue
					}
				}
				if postgres {
					end, closed := sqllex.PostgresStringEnd(sqlText, index)
					if !closed {
						return 0, fmt.Errorf("SQL 引用或注释未闭合")
					}
					index = end - 1
					continue
				}
				if rejectSeparators && backslashEscapes {
					end, err := unambiguousBackslashQuotedEnd(sqlText, index)
					if err != nil {
						return 0, err
					}
					index = end - 1
					continue
				}
				state = stateSingleQuote
			case '"':
				if rejectSeparators && backslashEscapes && !doubleQuoteIsIdentifier(dialect) {
					end, err := unambiguousBackslashQuotedEnd(sqlText, index)
					if err != nil {
						return 0, err
					}
					index = end - 1
					continue
				}
				state = stateDoubleQuote
			case '`':
				if !postgres {
					state = stateBacktick
				}
			case '[':
				// PostgreSQL 的数组构造、下标和切片仍是 SQL 正文，与 Rebind 保持一致。
				// 其他方言继续保留方括号标识符的引用规则。
				if !postgres {
					state = stateBracket
				}
			case '-':
				if index+1 < len(sqlText) && sqlText[index+1] == '-' {
					// MySQL 要求双横线后有空白或控制字符，否则它是运算符。
					if !mysql || index+2 == len(sqlText) || sqlText[index+2] <= ' ' || sqlText[index+2] == 0x7f {
						state = stateLineComment
						index++
					}
				}
			case '#':
				if mysql {
					state = stateLineComment
				}
			case '/':
				if index+1 < len(sqlText) && sqlText[index+1] == '*' {
					next, closed := skipSQLBlockComment(sqlText, index, dialect)
					if !closed {
						return 0, fmt.Errorf("SQL 引用或注释未闭合")
					}
					if rejectSeparators && mysql {
						comment := sqlText[index+2 : next-2]
						var code string
						executable := false
						if after, ok := strings.CutPrefix(comment, "!"); ok {
							code, executable = after, true
						} else if len(comment) >= 2 && strings.EqualFold(comment[:2], "M!") {
							code, executable = comment[2:], true
						}
						if executable {
							if _, err := scanSQLPlaceholders(code, true, dialects...); err != nil {
								return 0, err
							}
						}
					}
					index = next - 1
				}
			case '$':
				var delimiter string
				if postgres || dialect == "" {
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
			allowBackslashEscape := backslashEscapes &&
				(state == stateSingleQuote || (state == stateDoubleQuote && !doubleQuoteIsIdentifier(dialect)))
			if allowBackslashEscape && current == '\\' && index+1 < len(sqlText) {
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
			// SQLite 和 MySQL 的行注释只在 LF 或输入结尾终止，CR 仍属于注释内容。
			if current == '\n' || (current == '\r' && dialect != "sqlite" && dialect != "mysql") {
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

// unambiguousBackslashQuotedEnd 要求标准双写转义与反斜杠转义给出同一闭合边界，
// 防止 MySQL 会话开启 NO_BACKSLASH_ESCAPES 时提前闭合字符串并暴露后续语句分隔符。
func unambiguousBackslashQuotedEnd(sqlText string, start int) (int, error) {
	plainEnd, plainClosed := standardQuotedEnd(sqlText, start, false)
	escapedEnd, escapedClosed := standardQuotedEnd(sqlText, start, true)
	if !escapedClosed {
		return 0, fmt.Errorf("SQL 引用或注释未闭合")
	}
	if !plainClosed || plainEnd != escapedEnd {
		// 若在 NO_BACKSLASH_ESCAPES 边界之后出现分号，直接返回语句分隔符错误；否则拒绝歧义引号边界。
		if plainClosed && strings.IndexByte(sqlText[plainEnd:], ';') >= 0 {
			return 0, fmt.Errorf("原始条件不能包含 SQL 语句分隔符")
		}
		return 0, fmt.Errorf("原始条件不能包含依赖会话转义模式的引号边界")
	}
	return escapedEnd, nil
}

func standardQuotedEnd(sqlText string, start int, backslash bool) (int, bool) {
	closing := sqlText[start]
	for index := start + 1; index < len(sqlText); index++ {
		if backslash && sqlText[index] == '\\' {
			index++
			continue
		}
		if sqlText[index] != closing {
			continue
		}
		if index+1 < len(sqlText) && sqlText[index+1] == closing {
			index++
			continue
		}
		return index + 1, true
	}
	return len(sqlText), false
}
