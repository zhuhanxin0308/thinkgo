package db

import (
	"fmt"
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
	clauses []string
	args    []interface{}
	err     error
}

// NewConditionGroup 创建条件组。
func NewConditionGroup() *ConditionGroup {
	return &ConditionGroup{
		clauses: make([]string, 0),
		args:    make([]interface{}, 0),
	}
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
	g.clauses = appendConditionClause(g.clauses, "AND", clause)
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
	g.clauses = appendConditionClause(g.clauses, "AND", clause)
	g.args = append(g.args, args...)
	return g
}

func (g *ConditionGroup) append(connector string, condition interface{}, args ...interface{}) *ConditionGroup {
	if g.err != nil {
		return g
	}
	clause, compiledArgs, err := compileWhereExpression(condition, args)
	if err != nil {
		g.err = err
		return g
	}
	g.clauses = appendConditionClause(g.clauses, connector, clause)
	g.args = append(g.args, compiledArgs...)
	return g
}

func (g *ConditionGroup) compile() (string, []interface{}, error) {
	if g.err != nil {
		return "", nil, g.err
	}
	if len(g.clauses) == 0 {
		return "", nil, fmt.Errorf("condition group is empty")
	}
	if len(g.clauses) == 1 {
		return g.clauses[0], append([]interface{}(nil), g.args...), nil
	}
	clause := strings.Join(g.clauses, " AND ")
	return "(" + clause + ")", append([]interface{}(nil), g.args...), nil
}

func compileWhereExpression(condition interface{}, args []interface{}) (string, []interface{}, error) {
	switch typed := condition.(type) {
	case string:
		normalized, err := normalizePredicateClause(typed, len(args), "where")
		if err != nil {
			return "", nil, err
		}
		return normalized, append([]interface{}(nil), args...), nil
	case map[string]interface{}:
		return compileMapConditions(typed)
	case [][]interface{}:
		return compileTupleConditions(typed)
	case func(*ConditionGroup):
		group := NewConditionGroup()
		typed(group)
		return group.compile()
	default:
		return "", nil, fmt.Errorf("unsupported where condition type %T", condition)
	}
}

func compileMapConditions(conditions map[string]interface{}) (string, []interface{}, error) {
	if len(conditions) == 0 {
		return "", nil, fmt.Errorf("where map is empty")
	}

	keys := make([]string, 0, len(conditions))
	for key := range conditions {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	clauses := make([]string, 0, len(keys))
	args := make([]interface{}, 0, len(keys))
	for _, key := range keys {
		if err := validateIdentifier(key); err != nil {
			return "", nil, fmt.Errorf("unsafe where map field %q: %w", key, err)
		}
		clauses = append(clauses, fmt.Sprintf("%s = ?", key))
		args = append(args, conditions[key])
	}
	return strings.Join(clauses, " AND "), args, nil
}

func compileTupleConditions(conditions [][]interface{}) (string, []interface{}, error) {
	if len(conditions) == 0 {
		return "", nil, fmt.Errorf("where tuples are empty")
	}

	clauses := make([]string, 0, len(conditions))
	args := make([]interface{}, 0, len(conditions))
	for _, condition := range conditions {
		if len(condition) != 3 {
			return "", nil, fmt.Errorf("where tuple must be [field, operator, value]")
		}
		field, ok := condition[0].(string)
		if !ok {
			return "", nil, fmt.Errorf("where tuple field must be string")
		}
		operator, ok := condition[1].(string)
		if !ok {
			return "", nil, fmt.Errorf("where tuple operator must be string")
		}
		if err := validateIdentifier(field); err != nil {
			return "", nil, fmt.Errorf("unsafe where tuple field %q: %w", field, err)
		}
		if err := validateOperator(operator); err != nil {
			return "", nil, fmt.Errorf("unsafe where tuple operator %q: %w", operator, err)
		}
		clauses = append(clauses, fmt.Sprintf("%s %s ?", field, operator))
		args = append(args, condition[2])
	}
	return strings.Join(clauses, " AND "), args, nil
}

func appendConditionClause(existing []string, connector string, clause string) []string {
	if connector == "OR" && len(existing) > 0 {
		lastIndex := len(existing) - 1
		leftClause := unwrapCondition(existing[lastIndex])
		rightClause := unwrapCondition(clause)
		existing[lastIndex] = fmt.Sprintf("(%s OR %s)", leftClause, rightClause)
		return existing
	}
	return append(existing, clause)
}

func unwrapCondition(clause string) string {
	clause = strings.TrimSpace(clause)
	if strings.HasPrefix(clause, "(") && strings.HasSuffix(clause, ")") {
		return strings.TrimSpace(clause[1 : len(clause)-1])
	}
	return clause
}

func compileColumnClause(left string, op string, right string) (string, error) {
	if err := validateIdentifier(left); err != nil {
		return "", fmt.Errorf("unsafe whereColumn left field: %w", err)
	}
	if err := validateIdentifier(right); err != nil {
		return "", fmt.Errorf("unsafe whereColumn right field: %w", err)
	}
	if err := validateOperator(op); err != nil {
		return "", fmt.Errorf("unsafe whereColumn operator: %w", err)
	}
	return fmt.Sprintf("%s %s %s", left, op, right), nil
}

func compileExpressionClause(field string, op string, expression string) (string, error) {
	if err := validateIdentifier(field); err != nil {
		return "", fmt.Errorf("unsafe whereExp field: %w", err)
	}
	if err := validateOperator(op); err != nil {
		return "", fmt.Errorf("unsafe whereExp operator: %w", err)
	}
	if err := validateExpressionClause(expression); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s %s %s", field, op, strings.TrimSpace(expression)), nil
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

func buildTimeRange(kind string, now time.Time) (time.Time, time.Time, error) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "today":
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		end := start.Add(24*time.Hour - time.Second)
		return start, end, nil
	case "yesterday":
		end := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Add(-time.Second)
		start := time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, end.Location())
		return start, end, nil
	case "week":
		weekday := int(now.Weekday())
		if weekday == 0 {
			weekday = 7
		}
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -(weekday - 1))
		end := start.AddDate(0, 0, 7).Add(-time.Second)
		return start, end, nil
	case "month":
		start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		end := start.AddDate(0, 1, 0).Add(-time.Second)
		return start, end, nil
	case "year":
		start := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, now.Location())
		end := start.AddDate(1, 0, 0).Add(-time.Second)
		return start, end, nil
	default:
		return time.Time{}, time.Time{}, fmt.Errorf("unsupported time range %q", kind)
	}
}

func normalizeTimeValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case time.Time:
		return typed.Format(DefaultTimeFormat)
	default:
		return fmt.Sprint(value)
	}
}

