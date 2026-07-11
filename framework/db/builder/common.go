package builder

import (
	"strconv"
	"strings"
)

// quoteWith 用指定的开闭引用符包裹标识符，复杂表达式（已带引用符、*、函数调用、含空格）原样返回。
// 标识符在 Query 层已通过 validateIdentifier 校验，这里的引用主要用于规避与方言保留字冲突。
func quoteWith(name, open, closing string) string {
	if name == "*" || strings.Contains(name, open) ||
		strings.Contains(name, "(") || strings.Contains(name, " ") {
		return name
	}
	if strings.Contains(name, ".") {
		parts := strings.Split(name, ".")
		for index, part := range parts {
			parts[index] = open + part + closing
		}
		return strings.Join(parts, ".")
	}
	return open + name + closing
}

// quoteFieldsWith 处理逗号分隔的字段列表，含聚合函数或 AS 别名的复杂表达式整体原样返回。
func quoteFieldsWith(fields, open, closing string) string {
	if fields == "*" {
		return "*"
	}
	if strings.Contains(fields, "(") || strings.Contains(fields, " AS ") ||
		strings.Contains(fields, " as ") {
		return fields
	}
	parts := strings.Split(fields, ",")
	quoted := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		quoted = append(quoted, quoteWith(p, open, closing))
	}
	return strings.Join(quoted, ", ")
}

// rebindNumbered 把统一的 ? 占位符转换为带序号的占位符（如 $1、:1）。
// 与 sqlx.Rebind 一致，不处理出现在字符串字面量中的 ?（框架内 SQL 均为参数化构建，不含字面 ?）。
func rebindNumbered(query, prefix string) string {
	var b strings.Builder
	b.Grow(len(query) + 8)
	n := 0
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			n++
			b.WriteString(prefix)
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteByte(query[i])
	}
	return b.String()
}
