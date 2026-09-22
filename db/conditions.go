package db

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
)

// expressionCharPattern 限定表达式可用的字符集：标识符字符、空白、括号、占位符、
// 算术运算符、点/逗号/取模。刻意排除引号、分号、冒号与注释符，杜绝字符串字面量、
// 语句分隔与注释截断。
var expressionCharPattern = regexp.MustCompile(`^[A-Za-z0-9_\s\(\)\?\+\-\*\/\.,%]+$`)

// expressionWordPattern 提取表达式中的“单词” token（标识符或函数名）。
var expressionWordPattern = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_\.]*`)

// allowedExpressionFunctions 表达式中允许调用的安全函数白名单（无副作用的标量/聚合/时间函数）。
// 需要更复杂的函数请改用 WhereRaw 显式承担安全责任。
var allowedExpressionFunctions = map[string]bool{
	"now": true, "curdate": true, "curtime": true, "current_timestamp": true,
	"current_date": true, "current_time": true, "unix_timestamp": true,
	"count": true, "sum": true, "avg": true, "min": true, "max": true,
	"abs": true, "round": true, "floor": true, "ceil": true, "ceiling": true,
	"mod": true, "length": true, "char_length": true,
	"coalesce": true, "ifnull": true, "nullif": true, "greatest": true, "least": true,
	"date": true, "year": true, "month": true, "day": true, "hour": true, "minute": true, "second": true,
}

// bannedExpressionKeywords 即便不带空格、即便被括号包裹也会被识别为独立 token 而拒绝的关键字。
// 这是对“黑名单只匹配带空格关键字”绕过（如 (1)OR(1=1)）的纵深防御。
var bannedExpressionKeywords = map[string]bool{
	"select": true, "union": true, "insert": true, "update": true, "delete": true,
	"drop": true, "alter": true, "create": true, "truncate": true, "rename": true,
	"or": true, "and": true, "xor": true, "not": true,
	"from": true, "where": true, "join": true, "into": true, "values": true,
	"table": true, "database": true, "schema": true,
	"exec": true, "execute": true, "call": true, "declare": true,
	"case": true, "when": true, "then": true, "else": true, "end": true,
	"like": true, "in": true, "is": true, "between": true, "exists": true, "having": true,
	"sleep": true, "benchmark": true, "load_file": true, "outfile": true, "dumpfile": true,
	"information_schema": true, "null": true, "true": true, "false": true,
}

// ConditionGroup 用于构建闭包风格的嵌套条件。
type ConditionGroup struct {
	nodes     []PredicateNode
	arguments int
	err       error
}

// NewConditionGroup 创建条件组。
func NewConditionGroup() *ConditionGroup {
	return &ConditionGroup{}
}

// Where 以 AND 方式向条件组中追加条件。
func (g *ConditionGroup) Where(condition interface{}, args ...interface{}) *ConditionGroup {
	return g.append("AND", condition, args...)
}

// WhereOr 以 OR 方式向条件组中追加条件。
func (g *ConditionGroup) WhereOr(condition interface{}, args ...interface{}) *ConditionGroup {
	return g.append("OR", condition, args...)
}

// WhereColumn 追加字段与字段的比较条件。
func (g *ConditionGroup) WhereColumn(left string, op string, right string) *ConditionGroup {
	if g.err != nil {
		return g
	}
	clause, err := compileColumnClause(left, op, right)
	if err != nil {
		g.err = err
		return g
	}
	node, err := parsePredicateText(clause, nil)
	if err != nil {
		g.err = err
		return g
	}
	g.nodes = appendPredicateNode(g.nodes, "AND", node)
	return g
}

// WhereExp 追加字段与表达式的比较条件。
func (g *ConditionGroup) WhereExp(field string, op string, expression string, args ...interface{}) *ConditionGroup {
	if g.err != nil {
		return g
	}
	clause, err := compileExpressionClause(field, op, expression)
	if err != nil {
		g.err = err
		return g
	}
	if err := validatePlaceholderCount(expression, len(args)); err != nil {
		g.err = err
		return g
	}
	if err := validateArgumentBudget(g.arguments, len(args), maxQueryArguments); err != nil {
		g.err = err
		return g
	}
	node, err := parsePredicateText(clause, args)
	if err != nil {
		g.err = err
		return g
	}
	g.nodes = appendPredicateNode(g.nodes, "AND", node)
	g.arguments += len(args)
	return g
}

func (g *ConditionGroup) append(connector string, condition interface{}, args ...interface{}) *ConditionGroup {
	if g.err != nil {
		return g
	}
	node, count, err := compileWhereNode(condition, args)
	if err != nil {
		g.err = err
		return g
	}
	if err := validateArgumentBudget(g.arguments, count, maxQueryArguments); err != nil {
		g.err = err
		return g
	}
	g.nodes = appendPredicateNode(g.nodes, connector, node)
	g.arguments += count
	return g
}

func (g *ConditionGroup) compile() (string, []interface{}, error) {
	node, err := g.node()
	if err != nil {
		return "", nil, err
	}
	return predicateNodeSQL(node, nil)
}

func (g *ConditionGroup) node() (PredicateNode, error) {
	if g.err != nil {
		return PredicateNode{}, g.err
	}
	if len(g.nodes) == 0 {
		return PredicateNode{}, fmt.Errorf("condition group is empty")
	}
	if len(g.nodes) == 1 {
		return clonePredicateNodes(g.nodes)[0], nil
	}
	return PredicateNode{Kind: PredicateBoolean, Operator: "AND", Children: clonePredicateNodes(g.nodes)}, nil
}

func compileWhereNode(condition interface{}, args []interface{}) (PredicateNode, int, error) {
	switch typed := condition.(type) {
	case string:
		if err := validateArgumentBudget(0, len(args), maxQueryArguments); err != nil {
			return PredicateNode{}, 0, err
		}
		// 兼容 Where("field", "op", value) 三元组写法；操作符仍使用统一白名单，
		// 只把实际值加入绑定参数，不能借操作符位置注入 SQL。
		if len(args) == 2 && validateIdentifier(typed) == nil {
			operator, ok := args[0].(string)
			if !ok {
				return PredicateNode{}, 0, fmt.Errorf("where triplet operator must be string")
			}
			normalizedOperator, err := normalizeOperator(operator)
			if err != nil {
				return PredicateNode{}, 0, fmt.Errorf("unsafe where triplet operator %q: %w", operator, err)
			}
			return comparisonNode(typed, normalizedOperator, args[1]), 1, nil
		}
		if validateIdentifier(typed) == nil && len(args) == 1 {
			return comparisonNode(typed, "=", args[0]), 1, nil
		}
		normalized, err := normalizePredicateClause(typed, len(args), "where")
		if err != nil {
			return PredicateNode{}, 0, err
		}
		node, err := parsePredicateText(normalized, args)
		return node, len(args), err
	case map[string]interface{}:
		return compileMapNode(typed)
	case [][]interface{}:
		return compileTupleNodes(typed)
	case func(*ConditionGroup):
		group := NewConditionGroup()
		typed(group)
		node, err := group.node()
		return node, group.arguments, err
	default:
		return PredicateNode{}, 0, fmt.Errorf("unsupported where condition type %T", condition)
	}
}

func compileMapNode(conditions map[string]interface{}) (PredicateNode, int, error) {
	if len(conditions) == 0 {
		return PredicateNode{}, 0, fmt.Errorf("where map is empty")
	}
	if err := validateArgumentBudget(0, len(conditions), maxQueryArguments); err != nil {
		return PredicateNode{}, 0, err
	}

	keys := make([]string, 0, len(conditions))
	for key := range conditions {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	nodes := make([]PredicateNode, 0, len(keys))
	for _, key := range keys {
		if err := validateIdentifier(key); err != nil {
			return PredicateNode{}, 0, fmt.Errorf("unsafe where map field %q: %w", key, err)
		}
		nodes = append(nodes, comparisonNode(key, "=", conditions[key]))
	}
	if len(nodes) == 1 {
		return nodes[0], len(nodes), nil
	}
	return PredicateNode{Kind: PredicateBoolean, Operator: "AND", Children: nodes}, len(nodes), nil
}

func compileTupleNodes(conditions [][]interface{}) (PredicateNode, int, error) {
	if len(conditions) == 0 {
		return PredicateNode{}, 0, fmt.Errorf("where tuples are empty")
	}
	if err := validateArgumentBudget(0, len(conditions), maxQueryArguments); err != nil {
		return PredicateNode{}, 0, err
	}

	nodes := make([]PredicateNode, 0, len(conditions))
	for _, condition := range conditions {
		if len(condition) != 3 {
			return PredicateNode{}, 0, fmt.Errorf("where tuple must be [field, operator, value]")
		}
		field, ok := condition[0].(string)
		if !ok {
			return PredicateNode{}, 0, fmt.Errorf("where tuple field must be string")
		}
		operator, ok := condition[1].(string)
		if !ok {
			return PredicateNode{}, 0, fmt.Errorf("where tuple operator must be string")
		}
		if err := validateIdentifier(field); err != nil {
			return PredicateNode{}, 0, fmt.Errorf("unsafe where tuple field %q: %w", field, err)
		}
		normalizedOperator, err := normalizeOperator(operator)
		if err != nil {
			return PredicateNode{}, 0, fmt.Errorf("unsafe where tuple operator %q: %w", operator, err)
		}
		nodes = append(nodes, comparisonNode(field, normalizedOperator, condition[2]))
	}
	if len(nodes) == 1 {
		return nodes[0], len(nodes), nil
	}
	return PredicateNode{Kind: PredicateBoolean, Operator: "AND", Children: nodes}, len(nodes), nil
}

func compileColumnClause(left string, op string, right string) (string, error) {
	if err := validateIdentifier(left); err != nil {
		return "", fmt.Errorf("unsafe whereColumn left field: %w", err)
	}
	if err := validateIdentifier(right); err != nil {
		return "", fmt.Errorf("unsafe whereColumn right field: %w", err)
	}
	normalizedOperator, err := normalizeOperator(op)
	if err != nil {
		return "", fmt.Errorf("unsafe whereColumn operator: %w", err)
	}
	return fmt.Sprintf("%s %s %s", left, normalizedOperator, right), nil
}

func compileExpressionClause(field string, op string, expression string) (string, error) {
	if err := validateIdentifier(field); err != nil {
		return "", fmt.Errorf("unsafe whereExp field: %w", err)
	}
	normalizedOperator, err := normalizeOperator(op)
	if err != nil {
		return "", fmt.Errorf("unsafe whereExp operator: %w", err)
	}
	if err := validateExpressionClause(expression); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s %s %s", field, normalizedOperator, strings.TrimSpace(expression)), nil
}

// validateExpressionClause 以“字符白名单 + 逐 token 解析”的方式校验表达式，
// 取代原先基于带空格关键字子串的黑名单（可被 (1)OR(1=1) 这类去空格写法绕过）。
//
// 规则：
//  1. 仅允许有限字符集（无引号/分号/冒号/注释符），从源头杜绝字符串字面量与语句截断；
//  2. 每个“单词” token 若紧跟 '(' 视为函数调用，函数名必须在白名单内；
//  3. 其余单词 token 必须是合法标识符（列名，支持 table.col），且不得是被禁关键字；
//  4. 纯数字字面量与 '?' 占位符天然允许。
//
// 复杂表达式请改用 WhereRaw（调用方自行保证参数化与安全）。
func validateExpressionClause(expression string) error {
	expression = strings.TrimSpace(expression)
	if expression == "" {
		return fmt.Errorf("expression is empty")
	}
	if !expressionCharPattern.MatchString(expression) {
		return fmt.Errorf("unsafe expression %q", expression)
	}

	lower := strings.ToLower(expression)
	for _, loc := range expressionWordPattern.FindAllStringIndex(lower, -1) {
		word := lower[loc[0]:loc[1]]

		// 判断该 token 是否为函数调用（其后第一个非空白字符为 '('）。
		isFunc := false
		for i := loc[1]; i < len(lower); i++ {
			if lower[i] == ' ' || lower[i] == '\t' || lower[i] == '\n' || lower[i] == '\r' {
				continue
			}
			isFunc = lower[i] == '('
			break
		}

		if isFunc {
			if strings.Contains(word, ".") || !allowedExpressionFunctions[word] {
				return fmt.Errorf("unsafe expression function %q", word)
			}
			continue
		}

		if bannedExpressionKeywords[word] {
			return fmt.Errorf("unsafe expression keyword %q", word)
		}
		if err := validateIdentifier(word); err != nil {
			return fmt.Errorf("unsafe expression identifier %q", word)
		}
	}
	return nil
}

const (
	calendarDaysPerWeek = 7
)

// buildTimeWindow 返回按应用时区计算的半开时间窗口 [start, endExclusive)。
// 半开区间可以覆盖数据库支持的全部小数秒精度，也能正确处理 DST 跨日。
func buildTimeWindow(kind string, now time.Time) (time.Time, time.Time, error) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "today":
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		return start, start.AddDate(0, 0, 1), nil
	case "yesterday":
		end := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		return end.AddDate(0, 0, -1), end, nil
	case "week":
		weekday := int(now.Weekday())
		if weekday == 0 {
			weekday = 7
		}
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -(weekday - 1))
		return start, start.AddDate(0, 0, calendarDaysPerWeek), nil
	case "month":
		start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		return start, start.AddDate(0, 1, 0), nil
	case "year":
		start := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, now.Location())
		return start, start.AddDate(1, 0, 0), nil
	default:
		return time.Time{}, time.Time{}, fmt.Errorf("unsupported time range %q", kind)
	}
}

// buildTimeRange 保留旧的闭区间辅助函数，外部查询统一使用 buildTimeWindow。
func buildTimeRange(kind string, now time.Time) (time.Time, time.Time, error) {
	start, endExclusive, err := buildTimeWindow(kind, now)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return start, endExclusive.Add(-time.Second), nil
}

func normalizeTimeValue(value interface{}) (interface{}, error) {
	return normalizeTimeValueInLocation(value, time.UTC)
}

// normalizeTimeValueInLocation 按指定应用时区格式化显式时间条件，避免丢失业务日期语义。
func normalizeTimeValueInLocation(value interface{}, location *time.Location) (interface{}, error) {
	return normalizeTimeValueInStorage(value, location, TimestampValueTypeDateTime)
}

// normalizeTimeValueInStorage 按存储契约转换显式时间参数，保证 SQL 与 NoSQL 使用同一语义。
func normalizeTimeValueInStorage(value interface{}, location *time.Location, valueType string) (interface{}, error) {
	if location == nil {
		location = time.UTC
	}
	valueType = normalizeTimestampValueType(valueType)
	switch typed := value.(type) {
	case time.Time:
		if typed.IsZero() {
			return nil, fmt.Errorf("时间条件不能使用零值 time.Time")
		}
		converted := typed.In(location)
		switch valueType {
		case TimestampValueTypeUnix:
			return converted.Unix(), nil
		case TimestampValueTypeDate:
			return converted.Format(DefaultDateFormat), nil
		case TimestampValueTypeNative:
			return converted, nil
		default:
			return converted.Format(DefaultTimeFormat), nil
		}
	case string, []byte,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64:
		return value, nil
	case float32:
		numeric := float64(typed)
		if math.IsNaN(numeric) || math.IsInf(numeric, 0) {
			return nil, fmt.Errorf("时间条件不能使用非有限浮点数")
		}
		return value, nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return nil, fmt.Errorf("时间条件不能使用非有限浮点数")
		}
		return value, nil
	case nil:
		return nil, fmt.Errorf("时间条件值不能为空")
	default:
		return nil, fmt.Errorf("不支持的时间条件类型 %T", value)
	}
}
