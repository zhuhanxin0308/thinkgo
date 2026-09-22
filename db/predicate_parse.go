package db

import (
	"fmt"
	"regexp"
	"strings"
)

const maxPredicateDepth = 64

var predicateCollectionPattern = regexp.MustCompile(`(?i)^([A-Za-z_][A-Za-z0-9_.]*)\s+(NOT\s+IN|IN)\s*\(\s*\?(?:\s*,\s*\?)*\s*\)$`)
var predicateOperandPattern = regexp.MustCompile(`(?i)^([A-Za-z_][A-Za-z0-9_.]*)\s*(>=|<=|<>|!=|=|>|<)\s*(.+)$`)

func parsePredicateClauses(clauses []string, args []interface{}) (Predicate, error) {
	predicate := newPredicate()
	offset := 0
	for _, clause := range clauses {
		count, err := countSQLPlaceholders(clause)
		if err != nil {
			return Predicate{}, err
		}
		if count > len(args)-offset {
			return Predicate{}, fmt.Errorf("%w: 条件参数不足", ErrInvalidQuery)
		}
		node, err := parsePredicateText(clause, args[offset:offset+count])
		if err != nil {
			return Predicate{}, err
		}
		predicate.nodes = append(predicate.nodes, node)
		offset += count
	}
	if offset != len(args) {
		return Predicate{}, fmt.Errorf("%w: 条件参数数量不匹配", ErrInvalidQuery)
	}
	return predicate, nil
}

// parsePredicateText 仅在兼容文本输入边界解析一次，后端不再解析 SQL。
func parsePredicateText(expression string, args []interface{}) (PredicateNode, error) {
	if err := validateArgumentBudget(0, len(args), maxQueryArguments); err != nil {
		return PredicateNode{}, err
	}
	index := 0
	node, err := parsePredicateBranch(expression, args, &index, 0)
	if err != nil {
		return PredicateNode{}, fmt.Errorf("%w: %w", ErrInvalidQuery, err)
	}
	if index != len(args) {
		return PredicateNode{}, fmt.Errorf("%w: 条件参数数量不匹配", ErrInvalidQuery)
	}
	return node, nil
}

func parsePredicateBranch(expression string, args []interface{}, index *int, depth int) (PredicateNode, error) {
	if depth >= maxPredicateDepth {
		return PredicateNode{}, fmt.Errorf("条件分组超过深度限制")
	}
	for _, operator := range []string{"OR", "AND"} {
		parts, split, err := splitTopLevelBoolean(expression, operator)
		if err != nil {
			return PredicateNode{}, err
		}
		if split {
			node := PredicateNode{Kind: PredicateBoolean, Operator: operator}
			for _, part := range parts {
				child, err := parsePredicateBranch(part, args, index, depth+1)
				if err != nil {
					return PredicateNode{}, err
				}
				node.Children = append(node.Children, child)
			}
			return node, nil
		}
	}
	leaf, err := trimBalancedOuterParentheses(strings.TrimSpace(expression))
	if err != nil {
		return PredicateNode{}, err
	}
	if leaf == "1 = 0" {
		return PredicateNode{Kind: PredicateFalse}, nil
	}
	consume := func(count int) ([]interface{}, error) {
		if count < 0 || count > len(args)-*index {
			return nil, fmt.Errorf("条件参数不足")
		}
		values := cloneDatabaseValues(args[*index : *index+count])
		*index += count
		return values, nil
	}
	makeComparison := func(field, op string, count int) (PredicateNode, error) {
		if err := validateIdentifier(field); err != nil {
			return PredicateNode{}, err
		}
		values, err := consume(count)
		return PredicateNode{Kind: PredicateComparison, Field: field, Operator: op, Values: values}, err
	}
	if match := safeCompareClausePattern.FindStringSubmatch(leaf); match != nil {
		op, err := normalizeOperator(match[2])
		if err != nil {
			return PredicateNode{}, err
		}
		return makeComparison(match[1], op, 1)
	}
	if match := safeNullClausePattern.FindStringSubmatch(leaf); match != nil {
		op := "IS NULL"
		if strings.TrimSpace(match[2]) != "" {
			op = "IS NOT NULL"
		}
		return makeComparison(match[1], op, 0)
	}
	if match := safeBetweenClausePattern.FindStringSubmatch(leaf); match != nil {
		return makeComparison(match[1], "BETWEEN", 2)
	}
	if match := predicateCollectionPattern.FindStringSubmatch(leaf); match != nil {
		return makeComparison(match[1], strings.ToUpper(strings.Join(strings.Fields(match[2]), " ")), strings.Count(leaf, "?"))
	}
	if match := predicateOperandPattern.FindStringSubmatch(leaf); match != nil {
		if err := validateIdentifier(match[1]); err != nil {
			return PredicateNode{}, err
		}
		if validateIdentifier(match[3]) == nil {
			return PredicateNode{Kind: PredicateColumn, Field: match[1], Operator: match[2], RightField: match[3]}, nil
		}
		if err := validateExpressionClause(match[3]); err != nil {
			return PredicateNode{}, err
		}
		values, err := consume(strings.Count(match[3], "?"))
		return PredicateNode{Kind: PredicateExpression, Field: match[1], Operator: match[2], SQL: match[3], Values: values}, err
	}
	return PredicateNode{}, fmt.Errorf("条件不是受支持的参数化表达式: %q", leaf)
}
