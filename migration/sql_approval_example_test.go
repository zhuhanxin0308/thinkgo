package migration_test

import (
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
