package migration

import (
	"errors"
	"fmt"
)

// ErrMigrationNotApproved means the SQL definition does not match its trusted
// name-to-checksum approval. It is distinct from applied-history drift.
var ErrMigrationNotApproved = errors.New("SQL migration is not approved")

// NewSQLWithApproval constructs a SQL migration only when approvals contains
// its name and exact NewSQL checksum, covering normalized Up and Down statements.
// A missing, empty or mismatched approval returns nil and ErrMigrationNotApproved.
// Invalid definitions retain NewSQL's ErrInvalidMigration classification.
//
// Approvals must come from an independently reviewed, trusted source. Computing
// them from incoming SQL at startup defeats the check. This is neither an author
// signature nor a sandbox for SQL, Go initializers or programmatic migrations.
//
// Like NewSQL, construction performs no database I/O and snapshots the SQL slices.
// The approval map is read only during construction; later edits do not revoke
// returned migrations. Callers must not mutate the map during construction.
// Validate the entire plan before starting the runner; do not skip rejected entries.
func NewSQLWithApproval(name string, upStatements, downStatements []string, approvals map[string]string) (Migration, error) {
	current, err := NewSQL(name, upStatements, downStatements)
	if err != nil {
		return nil, err
	}
	expected, found := approvals[current.Name()]
	if !found || expected != current.Checksum() {
		return nil, fmt.Errorf("%w: %s", ErrMigrationNotApproved, current.Name())
	}
	return current, nil
}
