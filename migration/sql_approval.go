package migration

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrMigrationNotApproved means the SQL definition does not match its trusted
// name-to-digest approval. It is distinct from applied-history drift.
var ErrMigrationNotApproved = errors.New("SQL migration is not approved")

// NewSQLWithApproval constructs a SQL migration only when approvals contains
// its name and exact SQLApprovalDigest, covering normalized Up and Down statements.
// A missing, empty or mismatched approval returns nil and ErrMigrationNotApproved.
// Invalid definitions retain NewSQL's ErrInvalidMigration classification.
// Migration.Checksum is a separate legacy history value, not an approval digest.
//
// Approvals must come from an independently reviewed, trusted source. Computing
// them from incoming SQL at startup defeats the check. This is neither an author
// signature nor a sandbox for SQL, Go initializers or programmatic migrations.
//
// Like NewSQL, construction performs no database I/O and snapshots the SQL slices.
// The approval map is read only during construction; later edits do not revoke
// returned migrations. Callers must not mutate inputs during construction.
// Validate the entire plan before starting the runner; do not skip rejected entries.
func NewSQLWithApproval(name string, upStatements, downStatements []string, approvals map[string]string) (Migration, error) {
	current, digest, err := prepareSQLApproval(name, upStatements, downStatements)
	if err != nil {
		return nil, err
	}
	expected, found := approvals[current.Name()]
	if !found || expected != digest {
		return nil, fmt.Errorf("%w: %s", ErrMigrationNotApproved, current.Name())
	}
	return current, nil
}

// SQLApprovalDigest returns a versioned SHA-256 digest for independently reviewed
// SQL. It binds the migration name, directions, statement counts and byte lengths.
// Statements use NewSQL's whitespace normalization; all other bytes are preserved.
// The result is not Migration.Checksum and never replaces stored history values.
// Generate and protect approvals in a separate review process, not from incoming
// SQL at application startup. Invalid definitions return ErrInvalidMigration.
func SQLApprovalDigest(name string, upStatements, downStatements []string) (string, error) {
	_, digest, err := prepareSQLApproval(name, upStatements, downStatements)
	return digest, err
}

func prepareSQLApproval(name string, upStatements, downStatements []string) (Migration, string, error) {
	// Own the slices before either validation or hashing. NewSQL then snapshots
	// exactly these same strings for execution, with the same normalization.
	up := append([]string(nil), upStatements...)
	down := append([]string(nil), downStatements...)
	current, err := NewSQL(name, up, down)
	if err != nil {
		return nil, "", err
	}
	return current, sqlApprovalDigest(name, up, down), nil
}

func sqlApprovalDigest(name string, up, down []string) string {
	hash := sha256.New()
	writeUint := func(value uint64) {
		var encoded [8]byte
		binary.BigEndian.PutUint64(encoded[:], value)
		_, _ = hash.Write(encoded[:])
	}
	writeText := func(value string) {
		writeUint(uint64(len(value)))
		_, _ = io.WriteString(hash, value)
	}
	writeText("thinkgo/sql-migration-approval/v1")
	writeText(name)
	for _, group := range []struct {
		direction  string
		statements []string
	}{{"up", up}, {"down", down}} {
		writeText(group.direction)
		writeUint(uint64(len(group.statements)))
		for _, statement := range group.statements {
			writeText(strings.TrimSpace(statement))
		}
	}
	return "sql-approval-v1:" + hex.EncodeToString(hash.Sum(nil))
}
