package db

import (
	"errors"
	"strings"
	"testing"
)

func TestConnectionPoolBudgetValidation(t *testing.T) {
	for _, kind := range []string{"mysql", "pgsql", "sqlsrv", "sqlite", "oracle"} {
		for _, limit := range []int{0, 80} {
			config := Config{Type: kind, MaxOpenConns: 80, MaxIdleConns: 10, MaxOpenConnsLimit: limit}
			if err := config.Validate(); err != nil {
				t.Fatalf("%s limit=%d: %v", kind, limit, err)
			}
			cloned := config.clone()
			if cloned.MaxOpenConnsLimit != limit {
				t.Fatal("clone lost budget")
			}
			cloned.MaxOpenConnsLimit = 1
			if config.MaxOpenConnsLimit != limit {
				t.Fatal("copy mutated source budget")
			}
		}
	}
	for _, config := range []Config{
		{Type: "mysql", MaxOpenConnsLimit: -1},
		{Type: "mysql", MaxOpenConns: 81, MaxOpenConnsLimit: 80},
		{Type: "mongo", MaxOpenConnsLimit: 80},
		{Type: "neo4j", MaxOpenConnsLimit: 80},
		{Type: "custom", MaxOpenConnsLimit: 80},
	} {
		config.Password = "private-database-password"
		err := config.Validate()
		if !errors.Is(err, ErrInvalidDatabaseConfig) {
			t.Fatalf("expected invalid config for %s, %v", config.Type, err)
		}
		if strings.Contains(err.Error(), config.Password) {
			t.Fatal("budget error exposes credentials")
		}
	}
	for _, kind := range []string{"mongo", "neo4j", "custom"} {
		if err := (Config{Type: kind, MaxOpenConns: 1000000}).Validate(); err != nil {
			t.Fatal(err)
		}
	}
}
