package db

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
)

type literalLikeUnknownBuilder struct{ builder.Mysql }

func (*literalLikeUnknownBuilder) DialectName() string { return "unsupported" }

func TestLiteralLikeSQLDialects(t *testing.T) {
	literal := `50%_![ab]\尾'`
	for _, b := range []Builder{&builder.Mysql{}, &builder.Pgsql{}, &builder.Sqlite{}, &builder.Sqlsrv{}} {
		t.Run(b.DialectName(), func(t *testing.T) {
			node := PredicateNode{Kind: PredicateLikeLiteral, Field: "name", Values: []any{literal}}
			p := Predicate{nodes: []PredicateNode{node}, builder: b}
			where, args, err := p.compileSQL()
			want := `50!%!_!![ab]\尾'`
			if b.DialectName() == "sqlserver" {
				want = `50!%!_!!![ab]\尾'`
			}
			if err != nil || len(where) != 1 || where[0] != b.QuoteIdentifier("name")+" LIKE ? ESCAPE '!'" || !reflect.DeepEqual(args, []any{want}) {
				t.Fatalf("wrong dialect predicate: %q %#v %v", where, args, err)
			}
			if got := p.Clauses(); len(got) != 1 || got[0].SQL != where[0] || !reflect.DeepEqual(got[0].Args, args) {
				t.Fatalf("clause snapshot disagrees: %#v", got)
			}
			// Recompilation must consume the original text, never an already escaped cache.
			_, again, err := p.compileSQL(&builder.Pgsql{})
			if err != nil || !reflect.DeepEqual(again, []any{`50!%!_!![ab]\尾'`}) {
				t.Fatalf("recompilation double-escaped: %#v %v", again, err)
			}
			nodes, err := p.PortableNodes()
			if err != nil || nodes[0].Values[0] != literal {
				t.Fatal("native literal was replaced by SQL pattern")
			}
		})
	}
	if _, _, err := literalLikeSQL(PredicateNode{Values: []any{"x"}}, "name", &literalLikeUnknownBuilder{}); !errors.Is(err, ErrUnsupportedFeature) {
		t.Fatalf("unknown dialect accepted: %v", err)
	}
	for _, values := range [][]any{nil, {"x", "y"}, {1}} {
		if _, _, err := literalLikeSQL(PredicateNode{Values: values}, "name", &builder.Mysql{}); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("malformed node accepted: %v", err)
		}
	}
}

func TestLiteralLikeQuerySnapshotsAndValidation(t *testing.T) {
	connection, _ := newRecordingHardeningSQLConnection(t)
	db := NewDB(connection)
	connection.Builder = &builder.Sqlsrv{}
	base := db.Table("users").Where("tenant_id", 7)
	query := base.WhereLikeLiteral("name", `%_![ab]\用户`).WhereOr("enabled", true)
	p, err := query.Predicate()
	if err != nil {
		t.Fatal(err)
	}
	clauses, args, err := p.compileSQL()
	if err != nil || len(clauses) != 2 || !strings.Contains(clauses[1], "ESCAPE '!' OR") || len(args) != 3 || args[1] != `!%!_!!![ab]\用户` {
		t.Fatalf("boolean literal lost: %q %#v %v", clauses, args, err)
	}
	if len(base.where) != 1 || len(base.args) != 1 {
		t.Fatal("derived query mutated parent")
	}
	nodes, err := p.Nodes()
	if err != nil {
		t.Fatal(err)
	}
	nodes[1].Children[0].Values[0] = "changed"
	original, _ := p.Nodes()
	if original[1].Children[0].Values[0] != `%_![ab]\用户` {
		t.Fatal("node snapshot aliases query")
	}
	model := NewModel(db, "users").Where("tenant_id", 7).WhereLikeLiteral("name", "_")
	if model.query.err != nil || model.query.predicate.nodes[1].Kind != PredicateLikeLiteral {
		t.Fatal("model wrapper did not retain literal")
	}
	for _, bad := range []string{"x\x00%", string([]byte{0xff})} {
		if _, err := base.WhereLikeLiteral("name", bad).Predicate(); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("invalid text accepted: %v", err)
		}
	}
	if _, err := base.WhereLikeLiteral("name; DROP TABLE users", "x").Predicate(); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("unsafe field accepted: %v", err)
	}
}

func FuzzLiteralLikeEscapeRoundTrip(f *testing.F) {
	for _, s := range []string{"", "%", "_", "!", "[a-z]", `a\_%!['`, "客户😀_%", "line\n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 4096 || !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
			return
		}
		for _, b := range []Builder{&builder.Pgsql{}, &builder.Sqlsrv{}} {
			_, args, err := literalLikeSQL(PredicateNode{Values: []any{s}}, "name", b)
			if err != nil {
				t.Fatal(err)
			}
			pattern := args[0].(string)
			var decoded strings.Builder
			for i := 0; i < len(pattern); i++ {
				c := pattern[i]
				if c == '!' {
					i++
					if i == len(pattern) {
						t.Fatal("dangling escape")
					}
					c = pattern[i]
					if c != '!' && c != '%' && c != '_' && !(b.DialectName() == "sqlserver" && c == '[') {
						t.Fatal("invalid dialect escape")
					}
				} else if c == '%' || c == '_' || (b.DialectName() == "sqlserver" && c == '[') {
					t.Fatal("literal became a wildcard")
				}
				decoded.WriteByte(c)
			}
			if decoded.String() != s {
				t.Fatal("literal bytes changed")
			}
		}
	})
}
