package db

import (
	"fmt"
	"strings"
)

// PredicateKind 区分可移植条件和仅适用于 SQL 的显式表达式。
type PredicateKind uint8

const (
	PredicateComparison PredicateKind = iota + 1
	PredicateBoolean
	PredicateColumn
	PredicateExpression
	PredicateRaw
	PredicateFalse
)

// PredicateNode 是驱动可以读取的条件树快照，字段名尚未进行后端引用。
// SQL 仅用于 Raw 和显式 SQL 表达式，普通条件没有 SQL 文本中间表示。
type PredicateNode struct {
	Kind       PredicateKind
	Field      string
	Operator   string
	Values     []interface{}
	Children   []PredicateNode
	RightField string
	SQL        string
}

func clonePredicateNodes(nodes []PredicateNode) []PredicateNode {
	if nodes == nil {
		return nil
	}
	cloned := make([]PredicateNode, len(nodes))
	for index, node := range nodes {
		cloned[index] = node
		cloned[index].Values = cloneDatabaseValues(node.Values)
		cloned[index].Children = clonePredicateNodes(node.Children)
	}
	return cloned
}

func comparisonNode(field, operator string, values ...interface{}) PredicateNode {
	return PredicateNode{Kind: PredicateComparison, Field: field, Operator: operator, Values: cloneDatabaseValues(values)}
}

// appendPredicateNode 保持 WhereOr 与前一项组合的既有规则，嵌套节点始终保留分组边界。
func appendPredicateNode(nodes []PredicateNode, connector string, node PredicateNode) []PredicateNode {
	if connector == "OR" && len(nodes) > 0 {
		last := len(nodes) - 1
		nodes[last] = PredicateNode{Kind: PredicateBoolean, Operator: "OR", Children: []PredicateNode{nodes[last], node}}
		return nodes
	}
	return append(nodes, node)
}

func predicateNodeSQL(node PredicateNode, quote func(string) string) (string, []interface{}, error) {
	field := node.Field
	if quote != nil && field != "" {
		field = quote(field)
	}
	switch node.Kind {
	case PredicateFalse:
		return "1 = 0", nil, nil
	case PredicateBoolean:
		clauses := make([]string, 0, len(node.Children))
		var values []interface{}
		for _, child := range node.Children {
			clause, args, err := predicateNodeSQL(child, quote)
			if err != nil {
				return "", nil, err
			}
			clauses = append(clauses, clause)
			values = append(values, args...)
		}
		if len(clauses) == 0 {
			return "", nil, fmt.Errorf("%w: 条件组不能为空", ErrInvalidQuery)
		}
		return "(" + strings.Join(clauses, " "+node.Operator+" ") + ")", values, nil
	case PredicateColumn:
		right := node.RightField
		if quote != nil {
			right = quote(right)
		}
		return field + " " + node.Operator + " " + right, nil, nil
	case PredicateRaw:
		if err := validateRawClause(node.SQL, len(node.Values), "where"); err != nil {
			return "", nil, err
		}
		return node.SQL, cloneDatabaseValues(node.Values), nil
	case PredicateExpression:
		expression := node.SQL
		if quote != nil {
			expression = quoteConditionIdentifiers(expression, quote)
		}
		return field + " " + node.Operator + " " + expression, cloneDatabaseValues(node.Values), nil
	case PredicateComparison:
		switch node.Operator {
		case "IS NULL", "IS NOT NULL":
			return field + " " + node.Operator, nil, nil
		case "BETWEEN":
			return field + " BETWEEN ? AND ?", cloneDatabaseValues(node.Values), nil
		case "IN", "NOT IN":
			marks := make([]string, len(node.Values))
			for index := range marks {
				marks[index] = "?"
			}
			return field + " " + node.Operator + " (" + strings.Join(marks, ", ") + ")", cloneDatabaseValues(node.Values), nil
		default:
			return field + " " + node.Operator + " ?", cloneDatabaseValues(node.Values), nil
		}
	default:
		return "", nil, fmt.Errorf("%w: 未知条件节点", ErrInvalidQuery)
	}
}
