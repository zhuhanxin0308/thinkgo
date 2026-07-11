package command

import (
	"os"
	"path/filepath"
	"thinkgo/framework/console"
)

// Clear command
type Clear struct {
	console.Command
}

func (c *Clear) Configure() {
	c.Signature = "clear"
	c.Description = "Clear application cache"
}

func (c *Clear) Execute(input *console.Input, output *console.Output) {
	if c.App == nil || c.App.BasePath == "" {
		output.Error("Failed to clear cache: app base path is empty")
		return
	}

	// 先走缓存管理器，保证 Redis、自定义 file store 等当前 store 都按驱动语义清理。
	if c.App.Cache != nil {
		c.App.Cache.Flush()
	}

	// clear 命令只清理默认文件缓存目录，不能删除 runtime 下的日志、会话、证书和离线库。
	cachePath := filepath.Join(c.App.BasePath, "runtime", "cache")
	if err := os.RemoveAll(cachePath); err != nil {
		output.Error("Failed to clear cache: " + err.Error())
		return
	}
	if err := os.MkdirAll(cachePath, 0o700); err != nil {
		output.Error("Failed to recreate cache directory: " + err.Error())
		return
	}

	output.Success("Cache cleared successfully.")
}
