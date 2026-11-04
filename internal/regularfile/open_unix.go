//go:build unix

package regularfile

import (
	"os"
	"syscall"
)

func openCandidate(path string) (*os.File, error) {
	// #nosec G304 -- The caller authorizes the path; Open validates this descriptor before returning it.
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
