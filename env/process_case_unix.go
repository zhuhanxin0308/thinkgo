//go:build !windows

package env

// processEnvironmentKeyMatches 在大小写敏感的系统中只接纳规范键。
func processEnvironmentKeyMatches(key, normalized string) bool {
	return key == normalized
}
