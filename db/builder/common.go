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
// preservePostgresOperators 启用时还会还原 ??，并保留 ?|、?&、@? 操作符。
func rebindNumbered(query, prefix string, preservePostgresOperators ...bool) string {
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
	preserveOperators := len(preservePostgresOperators) > 0 && preservePostgresOperators[0]
	var output strings.Builder
	output.Grow(len(query) + 8)
	for index := 0; index < len(query); index++ {
		current := query[index]
		switch state {
		case stateNormal:
			switch current {
			case '?':
				if preserveOperators {
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
				state = stateSingle
			case '"':
				state = stateDouble
			case '`':
				state = stateBacktick
			case '[':
				state = stateBracket
			case '-':
				if index+1 < len(query) && query[index+1] == '-' {
					state = stateLineComment
				}
			case '/':
				if index+1 < len(query) && query[index+1] == '*' {
					state = stateBlockComment
				}
			case '$':
				if delimiter := builderDollarDelimiter(query[index:]); delimiter != "" {
					state = stateDollarQuote
					dollarDelimiter = delimiter
				}
			}
		case stateSingle, stateDouble, stateBacktick:
			closing := byte('\'')
			if state == stateDouble {
				closing = '"'
			} else if state == stateBacktick {
				closing = '`'
			}
			if current == '\\' && index+1 < len(query) {
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
				state = stateNormal
			}
		case stateBlockComment:
			if current == '*' && index+1 < len(query) && query[index+1] == '/' {
				output.WriteByte(current)
				index++
				output.WriteByte(query[index])
				state = stateNormal
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
