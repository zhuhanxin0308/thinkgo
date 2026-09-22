package db

import "testing"

func mustTestPredicate(t *testing.T, clauses []string, args []interface{}) Predicate {
	t.Helper()
	predicate := newPredicate()
	offset := 0
	for _, clause := range clauses {
		count, err := countSQLPlaceholders(clause)
		if err != nil || offset+count > len(args) {
			t.Fatalf("invalid test predicate %q: placeholders=%d offset=%d args=%d err=%v", clause, count, offset, len(args), err)
		}
		predicate = predicate.appendValidated(clause, args[offset:offset+count])
		offset += count
	}
	if offset != len(args) {
		t.Fatalf("unused test predicate args: used=%d total=%d", offset, len(args))
	}
	return predicate
}
