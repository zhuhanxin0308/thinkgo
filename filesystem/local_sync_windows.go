//go:build windows

package filesystem

import "os"

func syncLocalDirectory(_ *os.Root, _ string) error {
	// Windows 不提供与 Unix 目录 fsync 等价且可由 os.Root 安全调用的接口。
	// 文件内容已在替换前同步；此处保留成功语义，避免把已发布写入误报为失败。
	return nil
}
