package db

import "fmt"

// PredicateClause 是带来源信息的只读条件快照。
// UnsafeRaw 只会由显式 WhereRaw/HavingRaw 路径设置。
type PredicateClause struct {
	Connector string
	SQL       string
	Args      []interface{}
	UnsafeRaw bool
}

// Predicate 保存只能由框架安全入口或显式 Raw 入口产生的条件。
// clauses 不导出，调用方只能取得防御性副本。
type Predicate struct {
	clauses []PredicateClause
}

func newPredicate() Predicate {
	return Predicate{}
}

func (p Predicate) appendValidated(sql string, args []interface{}) Predicate {
	return p.append("AND", sql, args, false)
}

func (p Predicate) appendRaw(sql string, args []interface{}) Predicate {
	return p.append("AND", sql, args, true)
}

func (p Predicate) appendWithConnector(connector, sql string, args []interface{}, unsafeRaw bool) Predicate {
	return p.append(connector, sql, args, unsafeRaw)
}

func (p Predicate) append(connector, sql string, args []interface{}, unsafeRaw bool) Predicate {
	next := Predicate{clauses: clonePredicateClauses(p.clauses)}
	next.clauses = append(next.clauses, PredicateClause{
		Connector: connector,
		SQL:       sql,
		Args:      cloneDatabaseValues(args),
		UnsafeRaw: unsafeRaw,
	})
	return next
}

func (p Predicate) Clauses() []PredicateClause {
	return clonePredicateClauses(p.clauses)
}

func (p Predicate) Empty() bool {
	return len(p.clauses) == 0
}

func clonePredicateClauses(source []PredicateClause) []PredicateClause {
	if source == nil {
		return nil
	}
	cloned := make([]PredicateClause, len(source))
	for index, clause := range source {
		cloned[index] = PredicateClause{
			Connector: clause.Connector,
			SQL:       clause.SQL,
			Args:      cloneDatabaseValues(clause.Args),
			UnsafeRaw: clause.UnsafeRaw,
		}
	}
	return cloned
}

func (p Predicate) compileSQL() ([]string, []interface{}, error) {
	where := make([]string, 0, len(p.clauses))
	args := make([]interface{}, 0)
	for _, clause := range p.clauses {
		if clause.SQL == "" {
			return nil, nil, fmt.Errorf("%w: 条件不能为空", ErrInvalidQuery)
		}
		if err := validatePlaceholderCount(clause.SQL, len(clause.Args)); err != nil {
			return nil, nil, fmt.Errorf("%w: %w", ErrInvalidQuery, err)
		}
		connector := clause.Connector
		if connector == "" {
			connector = "AND"
		}
		where = appendConditionClause(where, connector, clause.SQL)
		args = append(args, cloneDatabaseValues(clause.Args)...)
	}
	return where, args, nil
}

func (p Predicate) compileNonSQL() ([]string, []interface{}, error) {
	for _, clause := range p.clauses {
		if clause.UnsafeRaw {
			return nil, nil, fmt.Errorf("%w: non-SQL drivers only accept framework-validated predicates", ErrUnsafeExpression)
		}
	}
	return p.compileSQL()
}
