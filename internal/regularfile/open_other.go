//go:build !unix

package regularfile

import "os"

func openCandidate(path string) (*os.File, error) {
	// Preserve the platform's ordinary read-only open semantics; no nonblocking guarantee is implied.
	// #nosec G304 -- The caller authorizes the path; Open validates this descriptor before returning it.
	return os.Open(path)
}
