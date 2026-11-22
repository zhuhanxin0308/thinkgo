package mongo

import (
	"fmt"
	"reflect"
	"regexp"
	"slices"

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
	guardMongoNegativeFields(filter)
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
	case PredicateLikeLiteral:
		field, err := mongoField(node.Field)
		if err != nil {
			return nil, err
		}
		if len(node.Values) != 1 {
			return nil, fmt.Errorf("%w: literal LIKE requires one value", ErrInvalidQuery)
		}
		literal, ok := node.Values[0].(string)
		if !ok || len(literal) > maxMongoLikeLength {
			return nil, fmt.Errorf("%w: invalid literal LIKE text", ErrInvalidQuery)
		}
		// Absolute anchors avoid the end-before-final-newline behavior of '$'.
		return bson.M{field: bson.M{"$regex": `\A` + regexp.QuoteMeta(literal) + `\z`, "$options": "i"}}, nil
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
		if len(node.Values) == 1 && isNilMongoScalar(node.Values[0]) && node.Operator != "IN" && node.Operator != "NOT IN" {
			switch node.Operator {
			case "=", "IS":
				return bson.M{field: nil}, nil
			case "!=", "<>", "IS NOT":
				return bson.M{field: bson.M{"$ne": nil}}, nil
			default:
				return nil, fmt.Errorf("%w: MongoDB 操作符 %q 不接受 NULL 值", ErrInvalidQuery, node.Operator)
			}
		}
		if node.Operator == "IS" || node.Operator == "IS NOT" {
			return nil, fmt.Errorf("%w: MongoDB 操作符 %q 仅支持 nil 值", ErrInvalidQuery, node.Operator)
		}
		switch node.Operator {
		case "IS NULL":
			return bson.M{field: nil}, nil
		case "IS NOT NULL":
			return bson.M{field: bson.M{"$ne": nil}}, nil
		case "IN":
			// PortableNodes 已复制参数。WHERE 中只有与非 NULL 候选相等才能为真。
			return bson.M{field: bson.M{"$in": slices.DeleteFunc(node.Values, isMongoNullCandidate)}}, nil
		case "NOT IN":
			if slices.ContainsFunc(node.Values, isMongoNullCandidate) {
				// 空 $in 恒假，同时保留原候选，后续仍会校验和转换主键。
				return bson.M{field: bson.M{"$in": []any{}, "$nin": node.Values}}, nil
			}
			return bson.M{field: bson.M{"$nin": node.Values}}, nil
		case "BETWEEN":
			if len(node.Values) != 2 || isNilMongoScalar(node.Values[0]) || isNilMongoScalar(node.Values[1]) {
				return nil, fmt.Errorf("%w: MongoDB BETWEEN 不接受 NULL 值", ErrInvalidQuery)
			}
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

// BSON 用有符号的单字节类型编号，MinKey 是 -1。集合只初始化一次，包含数组本身。
var mongoNonNullTypes = func() []int32 {
	const minKeyTypeCode int32 = -1
	types := []int32{minKeyTypeCode}
	for typ := bson.TypeDouble; typ <= bson.TypeMaxKey; typ++ {
		if typ.IsValid() && typ != bson.TypeNull {
			types = append(types, int32(typ))
		}
	}
	return types
}()

// 合取合并和主键别名校验完成后，才为每个否定条件添加叶字段的非空类型限制。
// $type 会穿过文档数组检查实际叶值；叶字段是数组时保留 MongoDB 的原生元素匹配。
func guardMongoNegativeFields(filter bson.M) {
	for field, value := range filter {
		switch condition := value.(type) {
		case []bson.M:
			for _, child := range condition {
				guardMongoNegativeFields(child)
			}
		case bson.M:
			if field == "$expr" || condition["$in"] != nil {
				continue
			}
			if condition["$ne"] != nil || condition["$not"] != nil || condition["$nin"] != nil {
				condition["$type"] = mongoNonNullTypes
			}
		}
	}
}

func isMongoNullCandidate(value any) bool {
	current := mongoScalarValue(value)
	return !current.IsValid() || current.Kind() == reflect.Slice && current.IsNil()
}

func isNilMongoScalar(value interface{}) bool {
	return !mongoScalarValue(value).IsValid()
}

func mongoScalarValue(value any) reflect.Value {
	current := reflect.ValueOf(value)
	for current.Kind() == reflect.Pointer || current.Kind() == reflect.Interface {
		current = current.Elem()
	}
	return current
}
