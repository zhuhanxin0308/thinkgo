package migration_test

import (
	"errors"
	"fmt"

	"github.com/zhuhanxin0308/thinkgo/v3/migration"
)

func ExampleNewSQLWithApproval() {
	// This value is pinned after reviewing both directions, not derived from
	// SQL arriving at startup. Protect changes to this approval separately.
	approvals := map[string]string{
		"202610120001_reviewed_table": "sql-approval-v1:e691b6cd4e47ba8f6638d7ef512e983def55127228274182ef3e35c901d2a281",
	}
	current, err := migration.NewSQLWithApproval("202610120001_reviewed_table",
		[]string{"CREATE TABLE approved_items (id INTEGER PRIMARY KEY)", "INSERT INTO approved_items (id) VALUES (51)"},
		[]string{"DROP TABLE approved_items"}, approvals)
	if err != nil {
		fmt.Println(err)
		return
	}
	registry := migration.NewRegistry()
	if err := registry.Register(current); err != nil {
		fmt.Println(err)
		return
	}
	// Construct and validate all definitions before starting the runner.
	fmt.Println(current.Name())
	// Output: 202610120001_reviewed_table
}

func ExampleNewSQLWithApproval_completePlan() {
	plan := []struct {
		name     string
		up, down []string
	}{
		{
			"202610120001_reviewed_table",
			[]string{"CREATE TABLE approved_items (id INTEGER PRIMARY KEY)", "INSERT INTO approved_items (id) VALUES (51)"},
			[]string{"DROP TABLE approved_items"},
		},
		{
			"202610120002_reviewed_index",
			[]string{"CREATE INDEX approved_items_id ON approved_items (id)"},
			[]string{"DROP INDEX approved_items_id"},
		},
	}
	buildRegistry := func(approvals map[string]string) (*migration.Registry, error) {
		// Validate every definition before exposing any executable plan.
		approved := make([]migration.Migration, 0, len(plan))
		for _, entry := range plan {
			current, err := migration.NewSQLWithApproval(entry.name, entry.up, entry.down, approvals)
			if err != nil {
				return nil, err
			}
			approved = append(approved, current)
		}
		registry := migration.NewRegistry()
		for _, current := range approved {
			if err := registry.Register(current); err != nil {
				return nil, err
			}
		}
		return registry, nil
	}
	// Both values are pinned by an independent review process.
	approvals := map[string]string{
		"202610120001_reviewed_table": "sql-approval-v1:e691b6cd4e47ba8f6638d7ef512e983def55127228274182ef3e35c901d2a281",
	}
	registry, err := buildRegistry(approvals)
	fmt.Println("incomplete plan withheld:", registry == nil && errors.Is(err, migration.ErrMigrationNotApproved))

	approvals["202610120002_reviewed_index"] = "sql-approval-v1:baa2e76b98bcda14afce28c951c5538f4da98cdbef71a1cafea251c60475bc8a"
	registry, err = buildRegistry(approvals)
	// Only now create a database store and runner; no prefix has been executed.
	fmt.Println("complete plan ready:", registry != nil && err == nil)
	// Output:
	// incomplete plan withheld: true
	// complete plan ready: true
}
