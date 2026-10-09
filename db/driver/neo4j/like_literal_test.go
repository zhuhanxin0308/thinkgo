package neo4j

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

func TestLiteralLikeNativePredicate(t *testing.T) {
	for _, literal := range []string{"", "%", "_", "[ab]", "a.b*", `\E.*`, "客户_%😀", "line\n", "line"} {
		connection := &Neo4jConnection{}
		predicate, err := db.NewDB(connection).Table("users").WhereLikeLiteral("name", literal).Predicate()
		if err != nil {
			t.Fatal(err)
		}
		clause, params, err := connection.compilePredicate(predicate)
		if err != nil {
			t.Fatal(err)
		}
		if len(params) != 1 || strings.Contains(clause, literal) && len(literal) > 4 {
			t.Fatal("literal interpolated into Cypher")
		}
		pattern, ok := params["w0"].(string)
		if !ok || !strings.HasPrefix(pattern, `(?i)\A`) || !strings.HasSuffix(pattern, `\z`) {
			t.Fatalf("bad absolute pattern: %#v", params)
		}
		matcher, err := regexp.Compile(pattern)
		if err != nil {
			t.Fatal(err)
		}
		if !matcher.MatchString(literal) || matcher.MatchString(literal+"x") || matcher.MatchString("x"+literal) || matcher.MatchString(literal+"\n") {
			t.Fatalf("literal %q expanded", literal)
		}
	}
	for _, values := range [][]any{nil, {"x", "y"}, {1}, {strings.Repeat("x", maxCypherLikeLength+1)}} {
		index := 0
		if _, err := compileCypherNode(PredicateNode{Kind: PredicateLikeLiteral, Field: "name", Values: values}, &index, map[string]any{}); !errors.Is(err, db.ErrInvalidQuery) {
			t.Fatalf("invalid native node accepted: %v", err)
		}
	}
}
