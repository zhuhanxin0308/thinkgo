package neo4j

import (
	"fmt"
	"strings"
)

func (c *Neo4jConnection) compilePredicate(predicate Predicate) (string, map[string]interface{}, error) {
	nodes, err := predicate.PortableNodes()
	if err != nil {
		return "", nil, err
	}
	params := make(map[string]interface{})
	clauses := make([]string, 0, len(nodes))
	index := 0
	for _, node := range nodes {
		clause, err := compileCypherNode(node, &index, params)
		if err != nil {
			return "", nil, err
		}
		clauses = append(clauses, clause)
	}
	return strings.Join(clauses, " AND "), params, nil
}

// compileCypherNode 从结构化条件生成参数化 Cypher，SQL 的语法不会进入后端。
func compileCypherNode(node PredicateNode, index *int, params map[string]interface{}) (string, error) {
	switch node.Kind {
	case PredicateFalse:
		return "false", nil
	case PredicateBoolean:
		clauses := make([]string, 0, len(node.Children))
		for _, child := range node.Children {
			clause, err := compileCypherNode(child, index, params)
			if err != nil {
				return "", err
			}
			clauses = append(clauses, clause)
		}
		return "(" + strings.Join(clauses, " "+node.Operator+" ") + ")", nil
	case PredicateComparison:
		field, err := cypherIdentifier(node.Field)
		if err != nil {
			return "", err
		}
		field = "n." + field
		if node.Operator == "IS NULL" || node.Operator == "IS NOT NULL" {
			return field + " " + node.Operator, nil
		}
		name := nextCypherParameter(index)
		switch node.Operator {
		case "IN", "NOT IN":
			params[name] = node.Values
			clause := field + " IN $" + name
			if node.Operator == "NOT IN" {
				clause = "NOT (" + clause + ")"
			}
			return clause, nil
		case "LIKE", "NOT LIKE":
			pattern, err := cypherLikeRegex(node.Values[0])
			if err != nil {
				return "", err
			}
			params[name] = pattern
			clause := field + " =~ $" + name
			if node.Operator == "NOT LIKE" {
				clause = "NOT (" + clause + ")"
			}
			return clause, nil
		case "BETWEEN":
			params[name+"_start"], params[name+"_end"] = node.Values[0], node.Values[1]
			return field + " >= $" + name + "_start AND " + field + " <= $" + name + "_end", nil
		case "=", "!=", "<>", ">", "<", ">=", "<=":
			operator := node.Operator
			if operator == "!=" {
				operator = "<>"
			}
			params[name] = node.Values[0]
			return field + " " + operator + " $" + name, nil
		default:
			return "", fmt.Errorf("%w: Neo4j 不支持操作符 %q", ErrInvalidQuery, node.Operator)
		}
	default:
		return "", fmt.Errorf("%w: Neo4j 不支持该条件节点", ErrInvalidQuery)
	}
}
