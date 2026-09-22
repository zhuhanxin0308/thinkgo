package framework

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/framework/filesystem"
)

// appFilesystemProvider 在 Cache 之后装配 ThinkPHP 文件系统管理器。
type appFilesystemProvider struct{}

// Register 保留 Provider 注册阶段，磁盘配置必须等待公共配置加载完成。
func (provider *appFilesystemProvider) Register(app *App) error {
	return nil
}

// Initialize 绑定文件系统管理器，但保持各磁盘的惰性创建语义。
func (provider *appFilesystemProvider) Initialize(app *App) error {
	if app == nil {
		return ErrNilApplication
	}
	if app.config == nil {
		return errors.New("应用配置实例不能为空")
	}
	if app.container == nil {
		return errors.New("应用容器实例不能为空")
	}
	configuration, err := app.filesystemConfiguration()
	if err != nil {
		return err
	}
	manager := filesystem.New(configuration)
	return wrapServiceBindingError(serviceKeyFilesystem, app.installManagedService(serviceKeyFilesystem, manager))
}

// Boot 不提前解析默认磁盘，首次业务调用 Disk 时再创建本地根目录。
func (provider *appFilesystemProvider) Boot(app *App) error {
	return nil
}

// Shutdown 释放统一登记的全部文件系统资源。
func (provider *appFilesystemProvider) Shutdown(app *App) error {
	if provider == nil {
		return nil
	}
	return app.closeServiceResources(serviceKeyFilesystem)
}

func (app *App) filesystemConfiguration() (map[string]interface{}, error) {
	if app == nil || app.config == nil {
		return nil, ErrNilApplication
	}
	configuration := defaultFilesystemConfiguration(app)
	if app.config.Has("filesystem") {
		raw := app.config.Get("filesystem")
		configured, ok := raw.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("filesystem 配置必须是对象")
		}
		configuration = configured
	}
	resolved := cloneFilesystemConfiguration(configuration)
	disks, ok := resolved["disks"].(map[string]interface{})
	if !ok {
		// 与 ThinkPHP 一致把具体磁盘错误延迟到首次 Disk 调用。
		return resolved, nil
	}
	for name, rawDisk := range disks {
		diskConfiguration, valid := rawDisk.(map[string]interface{})
		if !valid {
			continue
		}
		rawRoot, configured := diskConfiguration["root"]
		if !configured {
			continue
		}
		root, valid := rawRoot.(string)
		if !valid || strings.TrimSpace(root) == "" {
			continue
		}
		if !filepath.IsAbs(root) {
			root = filepath.Join(app.GetRootPath(), filepath.FromSlash(root))
		}
		diskConfiguration["root"] = filepath.Clean(root)
		disks[name] = diskConfiguration
	}
	return resolved, nil
}

func defaultFilesystemConfiguration(app *App) map[string]interface{} {
	return map[string]interface{}{
		"default": "local",
		"disks": map[string]interface{}{
			"local": map[string]interface{}{
				"type": "local",
				"root": filepath.Join(app.GetRuntimePath(), "storage"),
			},
			"public": map[string]interface{}{
				"type":       "local",
				"root":       filepath.Join(app.GetRootPath(), "public", "storage"),
				"url":        "/storage",
				"visibility": filesystem.VisibilityPublic,
			},
		},
	}
}

func cloneFilesystemConfiguration(configuration map[string]interface{}) map[string]interface{} {
	cloned := make(map[string]interface{}, len(configuration))
	for key, value := range configuration {
		switch current := value.(type) {
		case map[string]interface{}:
			cloned[key] = cloneFilesystemConfiguration(current)
		case []interface{}:
			items := make([]interface{}, len(current))
			for index, item := range current {
				if nested, ok := item.(map[string]interface{}); ok {
					items[index] = cloneFilesystemConfiguration(nested)
				} else {
					items[index] = item
				}
			}
			cloned[key] = items
		default:
			cloned[key] = current
		}
	}
	return cloned
}
