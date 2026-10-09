//go:build oracle

package db

import (
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
)

func TestLiteralLikeOracle(t *testing.T) {
	b := &builder.Oracle{}
	p := Predicate{nodes: []PredicateNode{{Kind: PredicateLikeLiteral, Field: "name", Values: []any{`[ab]_%!\x`}}}, builder: b}
	where, args, err := p.compileSQL()
	if err != nil || len(where) != 1 || where[0] != `"NAME" LIKE ? ESCAPE '!'` || len(args) != 1 || args[0] != `[ab]!_!%!!\x` {
		t.Fatalf("Oracle must escape only %%, _ and !: %q %#v %v", where, args, err)
	}
}
