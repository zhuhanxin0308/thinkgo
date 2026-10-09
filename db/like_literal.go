package db

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// WhereLikeLiteral matches the whole literal text, without interpreting user
// %, _, brackets or backslashes as pattern syntax. It does not add surrounding
// wildcards. LIKE collation, case handling and NULL semantics remain backend-defined.
// Invalid UTF-8 and NUL are rejected; use ordinary WhereLike for intentional patterns.
// This predicate does not replace tenant/object authorization conditions.
func (q *Query) WhereLikeLiteral(field, literal string) *Query {
	q = q.clone()
	if err := validateIdentifier(field); err != nil {
		return q.setError(fmt.Errorf("unsafe literal LIKE field: %w", err))
	}
	if !utf8.ValidString(literal) || strings.ContainsRune(literal, 0) {
		return q.setError(fmt.Errorf("%w: literal LIKE requires UTF-8 text without NUL", ErrInvalidQuery))
	}
	return q.appendQueryNode("AND", PredicateNode{Kind: PredicateLikeLiteral, Field: field, Values: []interface{}{literal}}, q.builder())
}

// WhereLikeLiteral adds a whole-text literal LIKE predicate to a model query.
// It preserves the model's scopes and does not interpret the supplied text as a pattern.
func (mq *ModelQuery) WhereLikeLiteral(field, literal string) *ModelQuery {
	mq = mq.clone()
	mq.query = mq.query.WhereLikeLiteral(field, literal)
	return mq
}

// literalLikeSQL chooses the escape grammar together with the actual SQL
// dialect. Returning a pattern alone would let callers omit the ESCAPE clause
// or reuse a SQL Server bracket escape on a backend that rejects it.
func literalLikeSQL(node PredicateNode, field string, builder Builder) (string, []interface{}, error) {
	// Native drivers build an unquoted compatibility snapshot here, but consume
	// the original literal node via PortableNodes rather than this SQL text.
	dialect := ""
	if builder != nil {
		dialect = builder.DialectName()
		switch dialect {
		case "mysql", "postgres", "sqlite", "sqlserver", "oracle":
		default:
			return "", nil, fmt.Errorf("%w: literal LIKE is not supported by dialect %q", ErrUnsupportedFeature, dialect)
		}
	}
	if len(node.Values) != 1 {
		return "", nil, fmt.Errorf("%w: literal LIKE requires one text value", ErrInvalidQuery)
	}
	literal, ok := node.Values[0].(string)
	if !ok {
		return "", nil, fmt.Errorf("%w: literal LIKE requires a string", ErrInvalidQuery)
	}
	var pattern strings.Builder
	pattern.Grow(len(literal))
	for index := 0; index < len(literal); index++ {
		character := literal[index]
		if character == '!' || character == '%' || character == '_' || (dialect == "sqlserver" && character == '[') {
			pattern.WriteByte('!')
		}
		pattern.WriteByte(character)
	}
	return field + " LIKE ? ESCAPE '!'", []interface{}{pattern.String()}, nil
}
