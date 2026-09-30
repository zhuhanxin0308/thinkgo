package builder

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	builderIdentifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_\.]*$`)
	builderAliasPattern      = regexp.MustCompile(`(?i)^(.+?)\s+AS\s+([A-Za-z_][A-Za-z0-9_]*)$`)
	builderAggregatePattern  = regexp.MustCompile(`(?i)^(COUNT|SUM|AVG|MIN|MAX)\(\s*(\*|[A-Za-z_][A-Za-z0-9_\.]*)\s*\)$`)
)

// quoteWith 逐段引用并转义闭合符，使任意输入都只能成为标识符内容。
func quoteWith(name, open, closing string) string {
	name = strings.TrimSpace(name)
	if name == "*" {
		return "*"
	}
	parts := strings.Split(name, ".")
	for index, part := range parts {
		// 限定通配符中的星号是 SQL 语法，不是普通列名，不能添加引用符。
		if part == "*" {
			continue
		}
		parts[index] = open + strings.ReplaceAll(part, closing, closing+closing) + closing
	}
	return strings.Join(parts, ".")
}

// quoteFieldsWith 只解析框架支持的标识符、AS 别名和单参数聚合函数。
func quoteFieldsWith(fields, open, closing string) string {
	fields = strings.TrimSpace(fields)
	if fields == "*" {
		return "*"
	}
	items := strings.Split(fields, ",")
	quoted := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		alias := ""
		if matches := builderAliasPattern.FindStringSubmatch(item); matches != nil {
			item = strings.TrimSpace(matches[1])
			alias = matches[2]
		}
		quotedExpression := quoteFieldExpression(item, open, closing)
		if alias != "" {
			quotedExpression += " AS " + quoteWith(alias, open, closing)
		}
		quoted = append(quoted, quotedExpression)
	}
	return strings.Join(quoted, ", ")
}

func quoteFieldExpression(expression, open, closing string) string {
	if expression == "1" {
		return expression
	}
	if expression == "*" || builderIdentifierPattern.MatchString(expression) {
		return quoteWith(expression, open, closing)
	}
	if matches := builderAggregatePattern.FindStringSubmatch(expression); matches != nil {
		return strings.ToUpper(matches[1]) + "(" + quoteWith(matches[2], open, closing) + ")"
	}
	// 未识别表达式整体作为一个引用标识符，宁可让数据库报列不存在，也不原样执行。
	return quoteWith(expression, open, closing)
}

func quoteOrderWith(order, open, closing string) string {
	if strings.TrimSpace(order) == "" {
		return ""
	}
	items := strings.Split(order, ",")
	quoted := make([]string, 0, len(items))
	for _, item := range items {
		parts := strings.Fields(item)
		if len(parts) == 0 {
			continue
		}
		if len(parts) > 2 || !builderIdentifierPattern.MatchString(parts[0]) {
			quoted = append(quoted, quoteWith(strings.TrimSpace(item), open, closing))
			continue
		}
		current := quoteWith(parts[0], open, closing)
		if len(parts) == 2 {
			direction := strings.ToUpper(parts[1])
			if direction != "ASC" && direction != "DESC" {
				quoted = append(quoted, quoteWith(strings.TrimSpace(item), open, closing))
				continue
			}
			current += " " + direction
		}
		quoted = append(quoted, current)
	}
	return strings.Join(quoted, ", ")
}

func sortedMapKeys(data map[string]interface{}) []string {
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// rebindNumbered 仅替换 SQL 正文中的问号，保留引用、注释和 dollar quote 内容。
// postgresSyntax 使用 standard_conforming_strings=on 的 PostgreSQL 词法规则。
// E 字符串支持反斜杠转义；普通字符串和引用标识符不把反斜杠视为引号转义。
func rebindNumbered(query, prefix string, postgresSyntax ...bool) string {
	const (
		stateNormal = iota
		stateSingle
		stateDouble
		stateBacktick
		stateBracket
		stateLineComment
		stateBlockComment
		stateDollarQuote
	)
	state := stateNormal
	dollarDelimiter := ""
	parameter := 0
	blockDepth := 0
	stringEscapes := false
	stringContinuation := false
	continuationNewline := false
	postgres := len(postgresSyntax) > 0 && postgresSyntax[0]
	var output strings.Builder
	output.Grow(len(query) + 8)
	for index := 0; index < len(query); index++ {
		current := query[index]
		switch state {
		case stateNormal:
			if postgres && stringContinuation {
				if current == '\n' || current == '\r' {
					continuationNewline = true
				}
				startsComment := index+1 < len(query) &&
					(current == '-' && query[index+1] == '-' || current == '/' && query[index+1] == '*')
				if current != '\'' && !builderRebindWhitespace(current) && !startsComment {
					stringContinuation = false
				}
			}
			switch current {
			case '?':
				if postgres {
					if index+1 < len(query) && query[index+1] == '?' {
						output.WriteByte('?')
						index++
						continue
					}
					if isPostgresQuestionOperator(query, index) {
						output.WriteByte('?')
						continue
					}
				}
				parameter++
				output.WriteString(prefix)
				output.WriteString(strconv.Itoa(parameter))
				continue
			case '\'':
				stringEscapes = !postgres || builderRebindEscapeString(query, index) ||
					stringContinuation && continuationNewline && stringEscapes
				stringContinuation = false
				state = stateSingle
			case '"':
				state = stateDouble
			case '`':
				if !postgres {
					state = stateBacktick
				}
			case '[':
				// PostgreSQL 数组构造、下标和切片中的参数仍属于 SQL 正文。
				if !postgres {
					state = stateBracket
				}
			case '-':
				if index+1 < len(query) && query[index+1] == '-' {
					state = stateLineComment
				}
			case '/':
				if index+1 < len(query) && query[index+1] == '*' {
					state = stateBlockComment
					blockDepth = 1
					// 一次消费整个起始符，禁止用其中的 * 与后续 / 重叠闭合。
					output.WriteString("/*")
					index++
					continue
				}
			case '$':
				if postgres && index > 0 && builderRebindIdentifierByte(query[index-1]) {
					break
				}
				if delimiter := builderDollarDelimiter(query[index:]); delimiter != "" {
					state = stateDollarQuote
					dollarDelimiter = delimiter
					// 起始分隔符不能同时成为结束分隔符的一部分（例如 $$$?$$）。
					output.WriteString(delimiter)
					index += len(delimiter) - 1
					continue
				}
			}
		case stateSingle, stateDouble, stateBacktick:
			closing := byte('\'')
			if state == stateDouble {
				closing = '"'
			} else if state == stateBacktick {
				closing = '`'
			}
			if current == '\\' && index+1 < len(query) && (!postgres || state == stateSingle && stringEscapes) {
				output.WriteByte(current)
				index++
				output.WriteByte(query[index])
				continue
			}
			if current == closing {
				if index+1 < len(query) && query[index+1] == closing {
					output.WriteByte(current)
					index++
					output.WriteByte(query[index])
					continue
				}
				stringContinuation = postgres && state == stateSingle
				continuationNewline = false
				state = stateNormal
			}
		case stateBracket:
			if current == ']' {
				if index+1 < len(query) && query[index+1] == ']' {
					output.WriteByte(current)
					index++
					output.WriteByte(query[index])
					continue
				}
				state = stateNormal
			}
		case stateLineComment:
			if current == '\n' || current == '\r' {
				continuationNewline = true
				state = stateNormal
			}
		case stateBlockComment:
			if current == '\n' || current == '\r' {
				continuationNewline = true
			}
			if postgres && current == '/' && index+1 < len(query) && query[index+1] == '*' {
				blockDepth++
				output.WriteString("/*")
				index++
				continue
			}
			if current == '*' && index+1 < len(query) && query[index+1] == '/' {
				output.WriteByte(current)
				index++
				output.WriteByte(query[index])
				blockDepth--
				if blockDepth == 0 {
					state = stateNormal
				}
				continue
			}
		case stateDollarQuote:
			if strings.HasPrefix(query[index:], dollarDelimiter) {
				output.WriteString(dollarDelimiter)
				index += len(dollarDelimiter) - 1
				state = stateNormal
				dollarDelimiter = ""
				continue
			}
		}
		output.WriteByte(current)
	}
	return output.String()
}

// isPostgresQuestionOperator 识别可直接出现在 SQL 正文中的 PostgreSQL 问号操作符。
func isPostgresQuestionOperator(query string, index int) bool {
	if index < 0 || index >= len(query) || query[index] != '?' {
		return false
	}
	if index+1 < len(query) && (query[index+1] == '|' || query[index+1] == '&') {
		return true
	}
	return index > 0 && query[index-1] == '@'
}

func builderDollarDelimiter(text string) string {
	if len(text) < 2 || text[0] != '$' {
		return ""
	}
	if text[1] == '$' {
		return "$$"
	}
	if !builderRebindIdentifierStart(text[1]) {
		return ""
	}
	for index := 2; index < len(text); index++ {
		current := text[index]
		if current == '$' {
			return text[:index+1]
		}
		if !builderRebindIdentifierStart(current) && !(current >= '0' && current <= '9') {
			return ""
		}
	}
	return ""
}

func builderRebindEscapeString(query string, quote int) bool {
	return quote > 0 && (query[quote-1] == 'E' || query[quote-1] == 'e') &&
		(quote == 1 || !builderRebindIdentifierByte(query[quote-2]))
}

// PostgreSQL 允许非 ASCII 字节组成标识符；$ 可出现在标识符中，但不能出现在 dollar quote 标签中。
func builderRebindIdentifierStart(character byte) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
		character == '_' || character >= 0x80
}

func builderRebindIdentifierByte(character byte) bool {
	return builderRebindIdentifierStart(character) || character >= '0' && character <= '9' || character == '$'
}

func builderRebindWhitespace(character byte) bool {
	switch character {
	case ' ', '\t', '\n', '\r', '\f', '\v':
		return true
	default:
		return false
	}
}
