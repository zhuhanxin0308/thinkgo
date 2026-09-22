package contract

import "errors"

var (
	ErrUnsupportedFeature  = errors.New("database feature is unsupported by the selected driver")
	ErrUnsupportedLockMode = errors.New("database lock mode is unsupported")
)

type LockMode uint8

const (
	LockNone LockMode = iota
	LockForUpdate
	LockForShare
)

type LockSpec struct {
	TableHint string
	Tail      string
}
