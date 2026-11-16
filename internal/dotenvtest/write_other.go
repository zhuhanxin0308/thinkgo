//go:build !windows

// Package dotenvtest 创建加载测试所需的私有环境文件夹具。
package dotenvtest

import "os"

// WriteFile 写入私有 .env 测试夹具。
func WriteFile(path string, contents []byte) error {
	return os.WriteFile(path, contents, 0o600)
}
