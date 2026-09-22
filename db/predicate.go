package db

import "fmt"

// PredicateClause 是按需生成的 SQL 兼容快照，不承担条件状态的存储职责。
type PredicateClause struct {
	Connector string
	SQL       string
	Args      []interface{}
	UnsafeRaw bool
}

// Predicate 仅保存不可变条件树；只有经过校验的框架入口可以创建内部节点。
type Predicate struct {
	nodes []PredicateNode
	err   error
	quote func(string) string
}

func newPredicate() Predicate { return Predicate{} }

func (p Predicate) clone() Predicate {
	p.nodes = clonePredicateNodes(p.nodes)
	return p
}

func (p Predicate) appendValidated(sql string, args []interface{}) Predicate {
	return p.appendWithConnector("AND", sql, args, false)
}

func (p Predicate) appendRaw(sql string, args []interface{}) Predicate {
	return p.appendWithConnector("AND", sql, args, true)
}

func (p Predicate) appendWithConnector(connector, sql string, args []interface{}, unsafeRaw bool) Predicate {
	p = p.clone()
	if p.err != nil {
		return p
	}
	var node PredicateNode
	if unsafeRaw {
		node = PredicateNode{Kind: PredicateRaw, SQL: sql, Values: cloneDatabaseValues(args)}
	} else {
		node, p.err = parsePredicateText(sql, args)
	}
	if p.err == nil {
		p.nodes = appendPredicateNode(p.nodes, connector, node)
	}
	return p
}

// Nodes 返回保留 Raw 来源的防御性树快照，供后端独立编译。
func (p Predicate) Nodes() ([]PredicateNode, error) {
	if p.err != nil {
		return nil, p.err
	}
	return clonePredicateNodes(p.nodes), nil
}

// PortableNodes 拒绝显式 Raw，避免由 SQL 文本伪装成可移植条件。
func (p Predicate) PortableNodes() ([]PredicateNode, error) {
	nodes, err := p.Nodes()
	if err != nil {
		return nil, err
	}
	var check func([]PredicateNode) error
	check = func(nodes []PredicateNode) error {
		for _, node := range nodes {
			if node.Kind == PredicateRaw {
				return fmt.Errorf("%w: 非 SQL 驱动拒绝 Raw 条件", ErrUnsafeExpression)
			}
			if err := check(node.Children); err != nil {
				return err
			}
		}
		return nil
	}
	return nodes, check(nodes)
}

func (p Predicate) Clauses() []PredicateClause {
	clauses := make([]PredicateClause, 0, len(p.nodes))
	for _, node := range p.nodes {
		sql, args, err := predicateNodeSQL(node, p.quote)
		if err != nil {
			return nil
		}
		clauses = append(clauses, PredicateClause{Connector: "AND", SQL: sql, Args: args, UnsafeRaw: predicateNodeIsRaw(node)})
	}
	return clauses
}

func predicateNodeIsRaw(node PredicateNode) bool {
	if node.Kind == PredicateRaw {
		return true
	}
	for _, child := range node.Children {
		if predicateNodeIsRaw(child) {
			return true
		}
	}
	return false
}

func (p Predicate) Empty() bool { return len(p.nodes) == 0 && p.err == nil }

func (p Predicate) compileSQL(builders ...Builder) ([]string, []interface{}, error) {
	if p.err != nil {
		return nil, nil, p.err
	}
	quote := p.quote
	if len(builders) > 0 && builders[0] != nil {
		quote = builders[0].QuoteIdentifier
	}
	where := make([]string, 0, len(p.nodes))
	var args []interface{}
	for _, node := range p.nodes {
		clause, values, err := predicateNodeSQL(node, quote)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %w", ErrInvalidQuery, err)
		}
		where, args = append(where, clause), append(args, values...)
	}
	return where, args, nil
}
