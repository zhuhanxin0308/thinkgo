package db

import (
	"fmt"
	"strings"
)

// splitTopLevelBoolean 按顶层 AND/OR 拆分安全条件，保留嵌套括号中的组合关系。
func splitTopLevelBoolean(expression, keyword string) ([]string, bool, error) {
	expression, err := trimBalancedOuterParentheses(strings.TrimSpace(expression))
	if err != nil {
		return nil, false, err
	}
	separator := " " + strings.ToUpper(keyword) + " "
	upper := strings.ToUpper(expression)
	depth := 0
	start := 0
	betweenPending := false
	parts := make([]string, 0, 2)
	for index := 0; index < len(expression); index++ {
		switch expression[index] {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return nil, false, fmt.Errorf("%w: 条件括号不平衡", ErrInvalidQuery)
			}
		default:
			if depth == 0 && strings.EqualFold(keyword, "AND") && strings.HasPrefix(upper[index:], " BETWEEN ") {
				betweenPending = true
				index += len(" BETWEEN ") - 1
				continue
			}
			if depth == 0 && strings.HasPrefix(upper[index:], separator) {
				if betweenPending {
					betweenPending = false
					index += len(separator) - 1
					continue
				}
				part := strings.TrimSpace(expression[start:index])
				if part == "" {
					return nil, false, fmt.Errorf("%w: 布尔条件包含空分支", ErrInvalidQuery)
				}
				parts = append(parts, part)
				betweenPending = false
				index += len(separator) - 1
				start = index + 1
			}
		}
	}
	if depth != 0 {
		return nil, false, fmt.Errorf("%w: 条件括号不平衡", ErrInvalidQuery)
	}
	if len(parts) == 0 {
		return []string{expression}, false, nil
	}
	last := strings.TrimSpace(expression[start:])
	if last == "" {
		return nil, false, fmt.Errorf("%w: 布尔条件包含空分支", ErrInvalidQuery)
	}
	parts = append(parts, last)
	return parts, true, nil
}

func trimBalancedOuterParentheses(expression string) (string, error) {
	for strings.HasPrefix(expression, "(") {
		depth := 0
		closesAtEnd := false
		for index := 0; index < len(expression); index++ {
			switch expression[index] {
			case '(':
				depth++
			case ')':
				depth--
				if depth < 0 {
					return "", fmt.Errorf("%w: 条件括号不平衡", ErrInvalidQuery)
				}
				if depth == 0 {
					closesAtEnd = index == len(expression)-1
					index = len(expression)
				}
			}
		}
		if depth != 0 {
			return "", fmt.Errorf("%w: 条件括号不平衡", ErrInvalidQuery)
		}
		if !closesAtEnd {
			break
		}
		expression = strings.TrimSpace(expression[1 : len(expression)-1])
	}
	if expression == "" {
		return "", fmt.Errorf("%w: 条件不能为空", ErrInvalidQuery)
	}
	return expression, nil
}
