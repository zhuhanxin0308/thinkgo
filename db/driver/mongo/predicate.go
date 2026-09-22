package mongo

import (
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func (c *MongoConnection) compilePredicate(predicate Predicate, primaryKey, modelKey string) (bson.M, error) {
	nodes, err := predicate.PortableNodes()
	if err != nil {
		return nil, err
	}
	filters := make([]bson.M, 0, len(nodes))
	for _, node := range nodes {
		filter, err := compileMongoNode(node)
		if err != nil {
			return nil, err
		}
		filters = append(filters, filter)
	}
	filter := combineMongoAnd(filters)
	if err := coerceMongoFilterPrimaryKeyAlias(filter, primaryKey, modelKey); err != nil {
		return nil, err
	}
	return filter, nil
}

// compileMongoNode 直接遍历条件树，不经过 SQL、占位符扫描或布尔文本解析。
func compileMongoNode(node PredicateNode) (bson.M, error) {
	switch node.Kind {
	case PredicateFalse:
		return bson.M{"$expr": bson.M{"$eq": bson.A{1, 0}}}, nil
	case PredicateBoolean:
		filters := make([]bson.M, 0, len(node.Children))
		for _, child := range node.Children {
			filter, err := compileMongoNode(child)
			if err != nil {
				return nil, err
			}
			filters = append(filters, filter)
		}
		if node.Operator == "OR" {
			return bson.M{"$or": filters}, nil
		}
		return combineMongoAnd(filters), nil
	case PredicateComparison:
		field, err := mongoField(node.Field)
		if err != nil {
			return nil, err
		}
		for _, value := range node.Values {
			if err := requireMongoScalar(value); err != nil {
				return nil, err
			}
		}
		switch node.Operator {
		case "IS NULL":
			return bson.M{field: nil}, nil
		case "IS NOT NULL":
			return bson.M{field: bson.M{"$ne": nil}}, nil
		case "IN":
			return bson.M{field: bson.M{"$in": node.Values}}, nil
		case "NOT IN":
			return bson.M{field: bson.M{"$nin": node.Values}}, nil
		case "BETWEEN":
			return bson.M{field: bson.M{"$gte": node.Values[0], "$lte": node.Values[1]}}, nil
		case "LIKE", "NOT LIKE":
			pattern, err := mongoLikeRegex(node.Values[0])
			if err != nil {
				return nil, err
			}
			if node.Operator == "NOT LIKE" {
				return bson.M{field: bson.M{"$not": pattern}}, nil
			}
			return bson.M{field: pattern}, nil
		case "=":
			return bson.M{field: node.Values[0]}, nil
		default:
			operator, ok := mongoComparisonOperators[node.Operator]
			if !ok {
				return nil, fmt.Errorf("%w: MongoDB 不支持操作符 %q", ErrInvalidQuery, node.Operator)
			}
			return bson.M{field: bson.M{operator: node.Values[0]}}, nil
		}
	default:
		return nil, fmt.Errorf("%w: MongoDB 不支持该条件节点", ErrInvalidQuery)
	}
}

var mongoComparisonOperators = map[string]string{">=": "$gte", "<=": "$lte", "!=": "$ne", "<>": "$ne", ">": "$gt", "<": "$lt"}
