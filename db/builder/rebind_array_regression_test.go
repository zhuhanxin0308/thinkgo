package builder

import "testing"

func TestRebindNumberedPostgresArrays(t *testing.T) {
	for _, test := range []struct {
		name  string
		query string
		want  string
	}{
		{"constructor", `SELECT ARRAY[?, ?], ?`, `SELECT ARRAY[$1, $2], $3`},
		{"subscript", `SELECT values[?] FROM t WHERE id = ?`, `SELECT values[$1] FROM t WHERE id = $2`},
		{"slice", `SELECT values[?:?], ? FROM t`, `SELECT values[$1:$2], $3 FROM t`},
		{"nested", `SELECT ARRAY[ARRAY[?, ?], ARRAY[?, ?]], ?`, `SELECT ARRAY[ARRAY[$1, $2], ARRAY[$3, $4]], $5`},
		{"quoted", `SELECT ARRAY['[?]', ?, 'it''s ?'], "odd[?]"[?], ?`, `SELECT ARRAY['[?]', $1, 'it''s ?'], "odd[?]"[$2], $3`},
		{"comment", `SELECT ARRAY[? /* ] ? */, ?], ?`, `SELECT ARRAY[$1 /* ] ? */, $2], $3`},
		{"dollar quote", `SELECT ARRAY[$tag$[?]$tag$, ?], ?`, `SELECT ARRAY[$tag$[?]$tag$, $1], $2`},
		{"operators", `SELECT data ?| ARRAY[?, ?], data ?& ARRAY[?], data ?? ?, data @? ?`, `SELECT data ?| ARRAY[$1, $2], data ?& ARRAY[$3], data ? $4, data @? $5`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := rebindNumbered(test.query, "$", true); got != test.want {
				t.Fatalf("Rebind(%q) = %q; want %q", test.query, got, test.want)
			}
		})
	}
}

func TestRebindNumberedRetainsSQLServerBracketIdentifiers(t *testing.T) {
	query := `SELECT [column?], [escaped]]?] FROM [table] WHERE id = ? AND flag = ?`
	want := `SELECT [column?], [escaped]]?] FROM [table] WHERE id = @p1 AND flag = @p2`
	if got := rebindNumbered(query, "@p"); got != want {
		t.Fatalf("Rebind = %q; want %q", got, want)
	}
}
