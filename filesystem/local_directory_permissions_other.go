//go:build !unix

package filesystem

import "os"

func validateLocalDirectoryMode(_ os.FileMode) error {
	return nil
}

func validateLocalDirectoryOwnership(_ os.FileInfo) error {
	// Windows modes do not describe DACL write/delete authority. Preserve the
	// existing platform contract: deployment must restrict directory ACLs to
	// trusted actors. Descriptor chmod and temporary identity checks still apply.
	return nil
}
