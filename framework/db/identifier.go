package db

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var (
	safeIdentifierPattern       = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	safeCompareClausePattern    = regexp.MustCompile(`(?i)^([A-Za-z_][A-Za-z0-9_\.]*)\s*(=|!=|<>|>=|<=|>|<|LIKE|NOT LIKE|IS|IS NOT)\s*\?$`)
	safeNullClausePattern       = regexp.MustCompile(`(?i)^([A-Za-z_][A-Za-z0-9_\.]*)\s+IS\s+(NOT\s+)?NULL$`)
	safeBetweenClausePattern    = regexp.MustCompile(`(?i)^([A-Za-z_][A-Za-z0-9_\.]*)\s+BETWEEN\s+\?\s+AND\s+\?$`)
)

// validateIdentifier 校验单个标识符，仅允许字母、数字、下划线和点分层级。
func validateIdentifier(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("identifier is empty")
	}

	for _, part := range strings.Split(value, ".") {
		if !safeIdentifierPattern.MatchString(part) {
			return fmt.Errorf("unsafe identifier %q", value)
		}
	}

	return nil
}

// validateIdentifierList 校验逗号分隔的字段列表，并允许显式 AS 别名。
func validateIdentifierList(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("identifier list is empty")
	}
	if value == "*" {
		return nil
	}

	for _, raw := range strings.Split(value, ",") {
		item := strings.TrimSpace(raw)
		if item == "" {
			return errors.New("identifier list contains empty item")
		}

		expr := item
		alias := ""
		lower := strings.ToLower(item)
		if index := strings.Index(lower, " as "); index >= 0 {
			expr = strings.TrimSpace(item[:index])
			alias = strings.TrimSpace(item[index+4:])
		}

		if err := validateIdentifier(expr); err != nil {
			return err
		}
		if alias != "" {
			if err := validateIdentifier(alias); err != nil {
				return err
			}
		}
	}

	return nil
}

// validateOrderClause 校验排序子句，只允许“字段 + 可选 ASC/DESC”的组合。
func validateOrderClause(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}

	for _, raw := range strings.Split(value, ",") {
		item := strings.TrimSpace(raw)
		if item == "" {
			return errors.New("order clause contains empty item")
		}

		parts := strings.Fields(item)
		if len(parts) == 0 || len(parts) > 2 {
			return fmt.Errorf("unsafe order clause %q", item)
		}
		if err := validateIdentifier(parts[0]); err != nil {
			return err
		}
		if len(parts) == 2 {
			direction := strings.ToUpper(parts[1])
			if direction != "ASC" && direction != "DESC" {
				return fmt.Errorf("unsafe order direction %q", parts[1])
			}
		}
	}

	return nil
}

// validateDataKeys 校验写入字段，避免把列名当成原始 SQL 片段拼接。
func validateDataKeys(data map[string]interface{}) error {
	for key := range data {
		if err := validateIdentifier(key); err != nil {
			return err
		}
	}
	return nil
}

// allowedOperators SQL 操作符白名单，只允许标准的比较和模糊匹配操作符。
var allowedOperators = map[string]bool{
	"=": true, "!=": true, "<>": true,
	">": true, ">=": true, "<": true, "<=": true,
	"LIKE": true, "like": true, "Like": true,
	"NOT LIKE": true, "not like": true, "Not Like": true,
	"IS": true, "is": true,
	"IS NOT": true, "is not": true,
}

// validateOperator 校验 SQL 操作符，仅允许白名单内的安全操作符，防止通过操作符位置注入 SQL。
func validateOperator(op string) error {
	if op == "" {
		return errors.New("operator is empty")
	}
	if !allowedOperators[op] {
		return fmt.Errorf("不安全的 SQL 操作符 %q，允许的操作符: =, !=, <>, >, >=, <, <=, LIKE, NOT LIKE, IS, IS NOT", op)
	}
	return nil
}


// joinConditionOperators JOIN 条件中允许的比较操作符（通常只需要等值和基本比较）。
var joinConditionOperators = map[string]bool{
	"=": true, "!=": true, "<>": true,
	">": true, ">=": true, "<": true, "<=": true,
}

// validateJoinCondition 校验 JOIN 条件表达式，仅允许 "field op field" 格式。
// 支持用 AND 连接多个条件，如 "a.id = b.user_id AND a.type = b.type"。
// 每个条件的两侧必须是合法标识符（支持 table.field 格式），中间必须是安全操作符。
func validateJoinCondition(condition string) error {
	condition = strings.TrimSpace(condition)
	if condition == "" {
		return errors.New("join condition is empty")
	}

	// 按 AND 分割条件（不区分大小写）
	parts := splitByAnd(condition)
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return errors.New("join condition contains empty clause")
		}

		// 尝试匹配 "left op right" 格式
		matched := false
		for op := range joinConditionOperators {
			// 用空格包裹操作符避免匹配字段名中的字符
			padded := " " + op + " "
			var left, right string
			if idx := strings.Index(part, padded); idx >= 0 {
				// 带空格形式：操作符两侧含空格，右操作数从 padded 之后开始。
				left = strings.TrimSpace(part[:idx])
				right = strings.TrimSpace(part[idx+len(padded):])
			} else {
				// 尝试无空格的紧凑格式（如 "a.id=b.id"）
				idx = strings.Index(part, op)
				if idx <= 0 || idx+len(op) >= len(part) {
					continue
				}
				// 确保操作符两侧不是其他操作符字符
				leftChar := part[idx-1]
				rightChar := part[idx+len(op)]
				if leftChar == '>' || leftChar == '<' || leftChar == '!' || leftChar == '=' {
					continue
				}
				if rightChar == '>' || rightChar == '<' || rightChar == '!' || rightChar == '=' {
					continue
				}
				left = strings.TrimSpace(part[:idx])
				right = strings.TrimSpace(part[idx+len(op):])
			}

			if left == "" || right == "" {
				continue
			}

			if err := validateIdentifier(left); err != nil {
				return fmt.Errorf("不安全的 JOIN 条件左侧标识符 %q: %w", left, err)
			}
			if err := validateIdentifier(right); err != nil {
				return fmt.Errorf("不安全的 JOIN 条件右侧标识符 %q: %w", right, err)
			}
			matched = true
			break
		}

		if !matched {
			return fmt.Errorf("不安全的 JOIN 条件 %q，仅允许 'field = field' 格式", part)
		}
	}

	return nil
}

// splitByAnd 按 AND 关键字（不区分大小写）分割条件字符串。
func splitByAnd(s string) []string {
	lower := strings.ToLower(s)
	var parts []string
	start := 0
	for {
		idx := strings.Index(lower[start:], " and ")
		if idx < 0 {
			parts = append(parts, s[start:])
			break
		}
		parts = append(parts, s[start:start+idx])
		start = start + idx + 5 // len(" and ")
	}
	return parts
}

// normalizePredicateClause 规范化默认的安全条件表达式。
// 仅允许以下几类形式：
// 1. field, value 速记写法，会被转换为 "field = ?"
// 2. "field op ?" 参数化比较
// 3. "field IS NULL" / "field IS NOT NULL"
// 4. "field BETWEEN ? AND ?"
// 更复杂的 SQL 需要显式走 WhereRaw / HavingRaw。
func normalizePredicateClause(condition string, argsCount int, clauseName string) (string, error) {
	condition = strings.TrimSpace(condition)
	if condition == "" {
		return "", fmt.Errorf("%s clause is empty", clauseName)
	}

	// 兼容旧版的 field,value 速记写法，同时把它规范化为参数化比较。
	if err := validateIdentifier(condition); err == nil {
		if argsCount != 1 {
			return "", fmt.Errorf("%s shorthand clause %q requires exactly 1 argument", clauseName, condition)
		}
		return condition + " = ?", nil
	}

	if matches := safeCompareClausePattern.FindStringSubmatch(condition); matches != nil {
		if err := validateIdentifier(matches[1]); err != nil {
			return "", fmt.Errorf("unsafe %s field: %w", clauseName, err)
		}
		if err := validateOperator(matches[2]); err != nil {
			return "", fmt.Errorf("unsafe %s operator: %w", clauseName, err)
		}
		if argsCount != 1 {
			return "", fmt.Errorf("%s clause %q requires exactly 1 argument", clauseName, condition)
		}
		return condition, nil
	}

	if matches := safeNullClausePattern.FindStringSubmatch(condition); matches != nil {
		if err := validateIdentifier(matches[1]); err != nil {
			return "", fmt.Errorf("unsafe %s field: %w", clauseName, err)
		}
		if argsCount != 0 {
			return "", fmt.Errorf("%s NULL clause %q does not accept arguments", clauseName, condition)
		}
		return condition, nil
	}

	if matches := safeBetweenClausePattern.FindStringSubmatch(condition); matches != nil {
		if err := validateIdentifier(matches[1]); err != nil {
			return "", fmt.Errorf("unsafe %s field: %w", clauseName, err)
		}
		if argsCount != 2 {
			return "", fmt.Errorf("%s BETWEEN clause %q requires exactly 2 arguments", clauseName, condition)
		}
		return condition, nil
	}

	return "", fmt.Errorf("unsafe %s clause %q, please use parameterized syntax or explicit raw API", clauseName, condition)
}
