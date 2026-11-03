// Package apppath resolves configured filesystem paths from the application root.
package apppath

import (
	"path/filepath"
	"strings"
)

// Resolve preserves absolute paths and anchors relative paths to basePath.
// The application constructor makes basePath absolute before configuration is read.
func Resolve(basePath, configured string) string {
	configured = strings.TrimSpace(configured)
	if filepath.IsAbs(configured) {
		return filepath.Clean(configured)
	}
	return filepath.Join(basePath, configured)
}
