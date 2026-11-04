// Package regularfile opens read-only file candidates and validates the resulting descriptor.
package regularfile

import (
	"errors"
	"os"
)

// ErrNotRegular indicates that the opened object is not an ordinary file.
var ErrNotRegular = errors.New("file is not regular")

// Open returns a read-only regular file and information from that same descriptor.
// The caller must authorize the path, verify any root or identity constraints,
// and close a successful result. Unix opens use O_NONBLOCK to avoid waiting for
// a FIFO peer; this is not a deadline for ordinary or network filesystem I/O.
func Open(path string) (*os.File, os.FileInfo, error) {
	file, err := openCandidate(path)
	if err != nil {
		return nil, nil, err
	}
	return validateOpened(file)
}

func validateOpened(file *os.File) (*os.File, os.FileInfo, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, nil, errors.Join(err, file.Close())
	}
	if !info.Mode().IsRegular() {
		return nil, nil, errors.Join(ErrNotRegular, file.Close())
	}
	return file, info, nil
}
