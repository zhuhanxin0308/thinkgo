package db

// CursorCodec defines how ChunkById verifies that a database cursor advances.
// The database remains responsible for WHERE/ORDER BY semantics; a codec must
// match the selected column's database type and collation.
type CursorCodec interface {
	Compare(previous, next interface{}) (int, error)
}

// OrderedCursorCodec is the conservative default for integer and time.Time keys.
// Text, byte, decimal-as-text, floating-point and custom keys require an explicit
// codec because Go ordering cannot prove equivalence with database ordering.
type OrderedCursorCodec struct{}

func (OrderedCursorCodec) Compare(previous, next interface{}) (int, error) {
	return compareOrderedDatabaseCursor(previous, next)
}
