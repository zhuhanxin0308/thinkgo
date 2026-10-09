//go:build cgo || integration

package db_test

import (
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

func literalLikeSamples() []string {
	return []string{"%", "_", "!", "[ab]", "a", "a_b", "axb", "100%", "100x", `C:\tmp`, "C:tmp", "客户_%", "客户abc", "emoji😀_", "quote' OR 1=1--", "line\n", "line"}
}

// The same row-set oracle runs against SQLite and the existing MySQL/PostgreSQL
// and native live contract entrypoints. Database collation is intentionally not normalized.
func assertLiteralLikeRows(t *testing.T, database *db.DB, table string) {
	t.Helper()
	samples := literalLikeSamples()
	rows := make([]map[string]any, 0, len(samples)+1)
	for index, text := range samples {
		rows = append(rows, map[string]any{"id": int64(index + 1), "name": text, "score": 7, "active": true, "created_at": time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)})
	}
	rows = append(rows, map[string]any{"id": int64(len(samples) + 1), "name": "%", "score": 99, "active": true, "created_at": time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)})
	if _, err := database.Table(table).InsertAll(rows); err != nil {
		t.Fatal(err)
	}
	scoped := database.Table(table).Where("score", 7)
	for _, literal := range samples {
		found, err := scoped.WhereLikeLiteral("name", literal).Select()
		if err != nil || len(found) != 1 {
			t.Fatalf("literal %q expanded or failed: rows=%d err=%v", literal, len(found), err)
		}
		var text string
		switch v := found[0]["name"].(type) {
		case string:
			text = v
		case []byte:
			text = string(v)
		default:
			t.Fatalf("unexpected text type %T", v)
		}
		if text != literal {
			t.Fatalf("literal %q matched %q", literal, text)
		}
	}
	if count, err := scoped.WhereLike("name", "%").Count(); err != nil || count != int64(len(samples)) {
		t.Fatalf("intentional wildcard contract changed: %d %v", count, err)
	}
	if count, err := scoped.Count(); err != nil || count != int64(len(samples)) {
		t.Fatalf("derived predicate changed scope: %d %v", count, err)
	}
	found, err := db.NewModel(database, table).Where("score", 7).WhereLikeLiteral("name", "%").SelectMaps()
	if err != nil || len(found) != 1 {
		t.Fatalf("model literal match expanded scope: %d %v", len(found), err)
	}
	if count, err := database.Table(table).Where("score", 99).Count(); err != nil || count != 1 {
		t.Fatalf("other owner's row was altered: %d %v", count, err)
	}
}
