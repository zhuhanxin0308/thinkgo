//go:build !unix

package http

import "os"

func openStaticCandidate(path string) (*os.File, error) {
	// #nosec G304 -- 调用方在返回句柄前执行 public 根目录、类型与身份校验。
	return os.Open(path)
}
