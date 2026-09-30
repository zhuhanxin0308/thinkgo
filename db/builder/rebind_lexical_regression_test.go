package builder

import (
	"strings"
	"testing"
)

func TestRebindNumberedPostgresLexicalBoundaries(t *testing.T) {
	for _, test := range []struct{ name, query, want string }{
		{"standard backslash", `SELECT '\', ?`, `SELECT '\', $1`},
		{"standard array", `SELECT ARRAY['\', ?], ?`, `SELECT ARRAY['\', $1], $2`},
		{"quoted identifier", `SELECT "column\", ? FROM t`, `SELECT "column\", $1 FROM t`},
		{"escape string", `SELECT E'it\'s ? \\', ?`, `SELECT E'it\'s ? \\', $1`},
		{"lowercase escape", `SELECT e'\'?\'', ?`, `SELECT e'\'?\'', $1`},
		{"escape continuation", "SELECT E'first'\n'it\\'s ?', ?", "SELECT E'first'\n'it\\'s ?', $1"},
		{"escape comment continuation", "SELECT E'first' -- ?\n'it\\'s ?', ?", "SELECT E'first' -- ?\n'it\\'s ?', $1"},
		{"escape mode ends", `SELECT E'first', '\', ?`, `SELECT E'first', '\', $1`},
		{"dollar overlap", `SELECT $$$?$$, ?`, `SELECT $$$?$$, $1`},
		{"empty dollar", `SELECT $$$$, ?`, `SELECT $$$$, $1`},
		{"dollar identifier", `SELECT price$tag$, ? FROM t`, `SELECT price$tag$, $1 FROM t`},
		{"unicode tag", `SELECT $文本$ ? [ ] $文本$, ?`, `SELECT $文本$ ? [ ] $文本$, $1`},
		{"tagged nesting", `SELECT $outer$ $inner$ ? $inner$ $outer$, ?`, `SELECT $outer$ $inner$ ? $inner$ $outer$, $1`},
		{"nested comment", `SELECT ? /* outer /* inner ? */ still ? */ , ?`, `SELECT $1 /* outer /* inner ? */ still ? */ , $2`},
		{"nested array comment", `SELECT ARRAY[? /* a /* b */ ? */ , ?], ?`, `SELECT ARRAY[$1 /* a /* b */ ? */ , $2], $3`},
		{"unclosed comment", `SELECT 1 /*/ ?`, `SELECT 1 /*/ ?`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := rebindNumbered(test.query, "$", true); got != test.want {
				t.Fatalf("Rebind(%q) = %q; want %q", test.query, got, test.want)
			}
		})
	}
}

// 将任意内容封装成合法字面量，验证内部问号不消耗后续数组参数的序号。
func FuzzPostgresRebindLexicalIsolation(f *testing.F) {
	for _, seed := range []string{"?", "$?", `\`, "it's ?", "/* ? */", "文本", ""} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if len(value) > 256 || strings.ContainsRune(value, 0) {
			return
		}
		standard := "'" + strings.ReplaceAll(value, "'", "''") + "'"
		escaped := "E'" + strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), "'", "''") + "'"
		literals := []string{standard, escaped}
		if !strings.Contains(value, "$literal$") {
			literals = append(literals, "$literal$"+value+"$literal$")
		}
		for _, literal := range literals {
			query := "SELECT " + literal + ", ARRAY[?, ?], ?"
			want := "SELECT " + literal + ", ARRAY[$1, $2], $3"
			if got := rebindNumbered(query, "$", true); got != want {
				t.Fatalf("literal was not isolated: got=%q want=%q", got, want)
			}
		}
	})
}
