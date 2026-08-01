package command

import (
	"fmt"

	"thinkgo/framework"
	cacheDriver "thinkgo/framework/cache/driver"
	"thinkgo/framework/console"
)

// Clear command
type Clear struct {
	console.Command
}

func (c *Clear) Configure() {
	c.Signature = "clear"
	c.Description = "Clear application cache"
	configureApplicationOption(&c.Command)
}

func (c *Clear) Execute(_ *console.Input, output *console.Output) error {
	if output == nil {
		return console.ErrInvalidOutput
	}
	if c.App == nil {
		return framework.ErrNilApplication
	}
	if c.App.BasePath == "" {
		return fmt.Errorf("failed to clear cache: app base path is empty")
	}

	// 先走缓存管理器，保证 Redis、自定义 file store 等当前 store 都按驱动语义清理。
	applicationCache, err := resolveApplicationCache(c.App)
	if err != nil {
		return fmt.Errorf("解析应用缓存失败: %w", err)
	}
	if applicationCache != nil {
		if err := applicationCache.Flush(); err != nil {
			return fmt.Errorf("failed to clear cache backend: %w", err)
		}
	}

	// runtime/cache 也使用文件驱动的受管清理，保留活动锁和目录内非缓存文件。
	cachePath := c.App.RuntimeCachePath()
	fileCache, err := cacheDriver.NewFile(cachePath)
	if err != nil {
		return fmt.Errorf("failed to open local cache directory: %w", err)
	}
	if err = fileCache.Clear(); err != nil {
		return fmt.Errorf("failed to clear local cache directory: %w", err)
	}

	output.Success("Cache cleared successfully.")
	return nil
}
