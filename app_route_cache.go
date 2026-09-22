package framework

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	frameworkRoute "github.com/zhuhanxin0308/thinkgo/framework/route"
)

// loadRouteNameCache 让后续应用进程消费命名路由优化产物，缺失缓存时继续使用编译路由。
func (app *App) loadRouteNameCache() error {
	path := filepath.Join(app.GetRuntimePath(), frameworkRoute.NameCacheFilename)
	content, exists, err := readApplicationOptimizationFile(app.GetRootPath(), path, frameworkRoute.MaxNameCacheBytes)
	if err != nil || !exists {
		return err
	}
	router, err := ResolveServiceAs[*frameworkRoute.Router](app, ServiceRoute)
	if err != nil {
		return err
	}
	return router.LoadNameCache(content)
}

// readApplicationOptimizationFile 约束缓存读取范围、文件类型和大小，避免通过优化产物读取项目外文件。
func readApplicationOptimizationFile(base, path string, maximum int64) ([]byte, bool, error) {
	relative, err := filepath.Rel(base, path)
	if err != nil || !filepath.IsLocal(relative) {
		return nil, false, fmt.Errorf("优化缓存路径必须位于项目根目录内")
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, false, err
	}
	defer root.Close()
	info, err := root.Lstat(relative)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() || info.Size() > maximum {
		return nil, false, fmt.Errorf("优化缓存必须是大小受限的普通文件")
	}
	file, err := root.Open(relative)
	if err != nil {
		return nil, false, err
	}
	content, readErr := io.ReadAll(io.LimitReader(file, maximum+1))
	if err := errors.Join(readErr, file.Close()); err != nil {
		return nil, false, err
	}
	if int64(len(content)) > maximum {
		return nil, false, fmt.Errorf("优化缓存超过大小限制")
	}
	return content, true, nil
}
