//go:build !oracle

package connector

// registerOracle 在未启用 Oracle 构建标签时保持空操作，避免默认构建引入 Oracle 客户端。
func registerOracle() {}
