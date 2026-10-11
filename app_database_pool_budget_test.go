package framework

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

func TestDatabasePoolBudgetConfigParsing(t *testing.T) {
	for _, value := range []interface{}{0, 80, int64(80), json.Number("80")} {
		raw := map[string]interface{}{"type": "mysql", "max_open_conns": 60, "max_open_conns_limit": value}
		got, err := readDatabaseConfig(raw)
		if err != nil {
			t.Fatalf("value=%v: %v", value, err)
		}
		if got.MaxOpenConns != 60 || (value != 0 && got.MaxOpenConnsLimit != 80) {
			t.Fatalf("parsed config=%+v", got)
		}
		raw["max_open_conns_limit"] = 1
		if got.MaxOpenConnsLimit == 1 {
			t.Fatal("parsed config aliases input")
		}
	}
	for _, value := range []interface{}{-1, 59, true, "80", nil, 80.5, json.Number("9223372036854775808")} {
		_, err := readDatabaseConfig(map[string]interface{}{"type": "mysql", "max_open_conns": 60, "max_open_conns_limit": value})
		if !errors.Is(err, db.ErrInvalidDatabaseConfig) {
			t.Fatalf("accepted budget %v: %v", value, err)
		}
	}
	for _, kind := range []string{"mongo", "neo4j"} {
		_, err := readDatabaseConfig(map[string]interface{}{"type": kind, "max_open_conns_limit": 80})
		if !errors.Is(err, db.ErrInvalidDatabaseConfig) {
			t.Fatalf("unsupported backend %s silently accepted budget: %v", kind, err)
		}
	}
}

func TestDatabasePoolBudgetBlocksLazyConnection(t *testing.T) {
	base := t.TempDir()
	writeTestAppConfigFiles(t, base)
	path := filepath.Join(base, "over-budget.db")
	raw := map[string]interface{}{"default": "primary", "connections": map[string]interface{}{
		"primary": map[string]interface{}{"type": "sqlite", "database": path, "max_open_conns": 81, "max_open_conns_limit": 80},
	}}
	content, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "config", "database.json"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	app := NewApp(base)
	defer func() { _ = app.Close() }()
	err = app.Initialize()
	if !errors.Is(err, db.ErrInvalidDatabaseConfig) {
		t.Fatalf("invalid lazy config not rejected at startup: %v", err)
	}
	if app.db != nil {
		t.Fatal("invalid config installed a database")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lazy startup opened database file: %v", err)
	}
}
