//go:build unix

package http

import (
	"os"
	"syscall"
)

// openStaticCandidate 必须在取得描述符前避免等待 FIFO 写端；先 Stat 再
// Open 无法抵御文件替换。调用方在同一描述符上复核普通文件类型和路径身份。
func openStaticCandidate(path string) (*os.File, error) {
	// #nosec G304 -- 调用方在返回句柄前执行 public 根目录、类型与身份校验。
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
