package db

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/v3/internal/sqllex"
)

var (
	havingAggregateOperandPattern = regexp.MustCompile(`(?i)^(COUNT|SUM|AVG|MIN|MAX)\(\s*(\*|(?:DISTINCT\s+)?[A-Za-z_][A-Za-z0-9_\.]*)\s*\)$`)
	havingCompareClausePattern    = regexp.MustCompile(`(?i)^(.+?)\s*(=|!=|<>|>=|<=|>|<|LIKE|NOT LIKE|IS|IS NOT)\s*\?$`)
	havingNullClausePattern       = regexp.MustCompile(`(?i)^(.+?)\s+IS\s+(NOT\s+)?NULL$`)
	havingBetweenClausePattern    = regexp.MustCompile(`(?i)^(.+?)\s+BETWEEN\s+\?\s+AND\s+\?$`)
)

func validateHavingOperand(operand string) (string, error) {
	operand = strings.TrimSpace(operand)
	if operand == "" {
		return "", fmt.Errorf("having operand is empty")
	}
	if err := validateIdentifier(operand); err == nil {
		return operand, nil
	}
	matches := havingAggregateOperandPattern.FindStringSubmatch(operand)
	if matches == nil {
		return "", fmt.Errorf("unsafe having operand %q", operand)
	}
	fn := strings.ToUpper(matches[1])
	argument := strings.TrimSpace(matches[2])
	if argument == "*" {
		if fn != "COUNT" {
			return "", fmt.Errorf("unsafe having aggregate %q", operand)
		}
		return "COUNT(*)", nil
	}
	upperArg := strings.ToUpper(argument)
	if strings.HasPrefix(upperArg, "DISTINCT") && len(argument) > len("DISTINCT") &&
		(argument[len("DISTINCT")] == ' ' || argument[len("DISTINCT")] == '\t' || argument[len("DISTINCT")] == '\n' || argument[len("DISTINCT")] == '\r') {
		column := strings.TrimSpace(argument[len("DISTINCT"):])
		if err := validateIdentifier(column); err != nil {
			return "", fmt.Errorf("unsafe having aggregate field: %w", err)
		}
		return fn + "(DISTINCT " + column + ")", nil
	}
	if err := validateIdentifier(argument); err != nil {
		return "", fmt.Errorf("unsafe having aggregate field: %w", err)
	}
	return fn + "(" + argument + ")", nil
}

func isNilScalarOperand(value interface{}) bool {
	if value == nil {
		return true
	}
	current := reflect.ValueOf(value)
	for current.Kind() == reflect.Pointer || current.Kind() == reflect.Interface {
		if current.IsNil() {
			return true
		}
		current = current.Elem()
	}
	return false
}

func compileHavingComparison(operand, op string, value interface{}) (string, []interface{}, error) {
	normalizedOperand, err := validateHavingOperand(operand)
	if err != nil {
		return "", nil, err
	}
	normalizedOperator, err := normalizeOperator(op)
	if err != nil {
		return "", nil, fmt.Errorf("unsafe having operator: %w", err)
	}
	if isNilScalarOperand(value) {
		switch normalizedOperator {
		case "=", "IS":
			return normalizedOperand + " IS NULL", nil, nil
		case "!=", "<>", "IS NOT":
			return normalizedOperand + " IS NOT NULL", nil, nil
		default:
			return "", nil, fmt.Errorf("%w: 操作符 %q 不接受 NULL 值", ErrInvalidQuery, normalizedOperator)
		}
	}
	return fmt.Sprintf("%s %s ?", normalizedOperand, normalizedOperator), []interface{}{value}, nil
}

func compileHavingClause(having string, args []interface{}) (string, []interface{}, error) {
	if err := validateArgumentBudget(0, len(args), maxQueryArguments); err != nil {
		return "", nil, err
	}
	trimmed := strings.TrimSpace(having)
	if trimmed == "" {
		return "", nil, fmt.Errorf("having clause is empty")
	}
	if len(args) == 2 {
		if operand, err := validateHavingOperand(trimmed); err == nil {
			operator, ok := args[0].(string)
			if !ok {
				return "", nil, fmt.Errorf("having triplet operator must be string")
			}
			return compileHavingComparison(operand, operator, args[1])
		}
	}
	if len(args) == 1 {
		if operand, err := validateHavingOperand(trimmed); err == nil {
			return compileHavingComparison(operand, "=", args[0])
		}
	}
	index := 0
	clause, compiledArgs, err := parseHavingBranch(trimmed, args, &index, 0)
	if err != nil {
		return "", nil, err
	}
	if index != len(args) {
		return "", nil, fmt.Errorf("having clause %q argument count mismatch", having)
	}
	return clause, compiledArgs, nil
}

func parseHavingBranch(expression string, args []interface{}, index *int, depth int) (string, []interface{}, error) {
	if depth >= maxPredicateDepth {
		return "", nil, fmt.Errorf("having 条件分组超过深度限制")
	}
	for _, operator := range []string{"OR", "AND"} {
		parts, split, err := splitTopLevelBoolean(expression, operator)
		if err != nil {
			return "", nil, err
		}
		if split {
			clauses := make([]string, 0, len(parts))
			var branchArgs []interface{}
			for _, part := range parts {
				childClause, childArgs, err := parseHavingBranch(part, args, index, depth+1)
				if err != nil {
					return "", nil, err
				}
				if operator == "AND" && hasTopLevelSQLOr(childClause, "") {
					childClause = "(" + childClause + ")"
				}
				clauses = append(clauses, childClause)
				branchArgs = append(branchArgs, childArgs...)
			}
			joined := strings.Join(clauses, " "+operator+" ")
			if depth > 0 {
				joined = "(" + joined + ")"
			}
			return joined, branchArgs, nil
		}
	}
	leaf, err := trimBalancedOuterParentheses(strings.TrimSpace(expression))
	if err != nil {
		return "", nil, err
	}
	if matches := havingNullClausePattern.FindStringSubmatch(leaf); matches != nil {
		operand, err := validateHavingOperand(matches[1])
		if err != nil {
			return "", nil, fmt.Errorf("unsafe having field: %w", err)
		}
		if strings.TrimSpace(matches[2]) != "" {
			return operand + " IS NOT NULL", nil, nil
		}
		return operand + " IS NULL", nil, nil
	}
	if matches := havingBetweenClausePattern.FindStringSubmatch(leaf); matches != nil {
		operand, err := validateHavingOperand(matches[1])
		if err != nil {
			return "", nil, fmt.Errorf("unsafe having field: %w", err)
		}
		if *index+2 > len(args) {
			return "", nil, fmt.Errorf("having BETWEEN clause %q requires exactly 2 arguments", leaf)
		}
		minVal, maxVal := args[*index], args[*index+1]
		*index += 2
		if isNilScalarOperand(minVal) || isNilScalarOperand(maxVal) {
			return "", nil, fmt.Errorf("%w: HAVING BETWEEN 不接受 NULL 值", ErrInvalidQuery)
		}
		return operand + " BETWEEN ? AND ?", []interface{}{minVal, maxVal}, nil
	}
	if matches := havingCompareClausePattern.FindStringSubmatch(leaf); matches != nil {
		if *index+1 > len(args) {
			return "", nil, fmt.Errorf("having clause %q requires 1 argument", leaf)
		}
		value := args[*index]
		*index++
		return compileHavingComparison(matches[1], matches[2], value)
	}
	return "", nil, fmt.Errorf("unsafe having clause %q, please use parameterized syntax or explicit raw API", leaf)
}

func joinHavingAnd(existing, addition, dialect string) string {
	if existing == "" {
		return addition
	}
	if hasTopLevelSQLOr(existing, dialect) {
		existing = "(" + existing + ")"
	}
	if hasTopLevelSQLOr(addition, dialect) {
		addition = "(" + addition + ")"
	}
	return existing + " AND " + addition
}

// hasTopLevelSQLOr 扫描 SQL 条件在顶层（非引号、非注释、括号深度为 0）是否包含布尔 OR，
// 防止与后续 AND 拼接时改变布尔优先级。
func hasTopLevelSQLOr(sqlText, dialect string) bool {
	dialect = strings.ToLower(strings.TrimSpace(dialect))
	backslashEscapes := sqlBackslashEscapes(dialect)
	postgres := dialect == "postgres"
	mysql := dialect == "mysql"
	depth := 0
	for index := 0; index < len(sqlText); index++ {
		current := sqlText[index]
		switch current {
		case '\'':
			if dialect == "oracle" {
				if end, _ := sqllex.OracleAlternativeStringEnd(sqlText, index); end != index {
					index = end - 1
					continue
				}
			}
			if postgres {
				end, _ := sqllex.PostgresStringEnd(sqlText, index)
				index = end - 1
				continue
			}
			end, _ := standardQuotedEnd(sqlText, index, backslashEscapes)
			index = end - 1
		case '"':
			end, _ := standardQuotedEnd(sqlText, index, backslashEscapes && !doubleQuoteIsIdentifier(dialect))
			index = end - 1
		case '`':
			if !postgres {
				end, _ := standardQuotedEnd(sqlText, index, false)
				index = end - 1
			}
		case '[':
			if !postgres {
				for index++; index < len(sqlText); index++ {
					if sqlText[index] == ']' {
						if index+1 < len(sqlText) && sqlText[index+1] == ']' {
							index++
							continue
						}
						break
					}
				}
			}
		case '-':
			if index+1 < len(sqlText) && sqlText[index+1] == '-' &&
				(!mysql || index+2 == len(sqlText) || sqlText[index+2] <= ' ' || sqlText[index+2] == 0x7f) {
				index += 2
				for index < len(sqlText) && sqlText[index] != '\n' && (sqlText[index] != '\r' || dialect == "sqlite" || dialect == "mysql") {
					index++
				}
			}
		case '#':
			if mysql {
				for index++; index < len(sqlText) && sqlText[index] != '\n'; index++ {
				}
			}
		case '/':
			if index+1 < len(sqlText) && sqlText[index+1] == '*' {
				end, _ := skipSQLBlockComment(sqlText, index, dialect)
				index = end - 1
			}
		case '$':
			if postgres || dialect == "" {
				if delimiter := sqllex.PostgresDollarDelimiter(sqlText, index); delimiter != "" {
					index += len(delimiter)
					if closing := strings.Index(sqlText[index:], delimiter); closing >= 0 {
						index += closing + len(delimiter) - 1
					} else {
						return false
					}
				}
			}
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 && index+2 <= len(sqlText) && strings.EqualFold(sqlText[index:index+2], "OR") {
				prevWord := index > 0 && isSQLWordChar(sqlText[index-1])
				nextWord := index+2 < len(sqlText) && isSQLWordChar(sqlText[index+2])
				if !prevWord && !nextWord {
					return true
				}
			}
		}
	}
	return false
}

func isSQLWordChar(ch byte) bool {
	return ch == '_' || ch == '$' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9'
}
