package command

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	framework "github.com/zhuhanxin0308/thinkgo/v3"
)

const discoveryOverlayFilename = "overlay.json"

// writeControllerDiscoveryOverlay 将目标清单写入独占目录，由 Go 编译器映射到项目逻辑路径。
// 不复制项目或改写 go.mod，因此相对 replace、嵌入资源及构建标签仍按原项目解释。
func writeControllerDiscoveryOverlay(app *framework.App, directory string) (returnErr error) {
	controllerDiscoveryTransactionMu.Lock()
	defer controllerDiscoveryTransactionMu.Unlock()
	sources, err := nativeApplicationDiscoverySources(app)
	if err != nil {
		return err
	}
	base, err := filepath.Abs(app.BasePath)
	if err != nil {
		return err
	}
	directory, err = filepath.Abs(directory)
	if err != nil {
		return err
	}
	// 仅接收本次新建目录；绝不覆盖已有目录中的用户文件。
	if err := os.Mkdir(directory, 0o700); err != nil {
		return fmt.Errorf("创建发现 overlay 目录失败: %w", err)
	}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, os.RemoveAll(directory))
		}
	}()
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, root.Close()) }()
	configuration := struct {
		Replace map[string]string
	}{Replace: make(map[string]string, len(sources))}
	for _, generated := range sources {
		relative, err := validateControllerDiscoveryRelativePath(generated.relativePath)
		if err != nil {
			return err
		}
		if err := root.MkdirAll(filepath.Dir(relative), 0o700); err != nil {
			return err
		}
		if err := root.WriteFile(relative, generated.source, 0o600); err != nil {
			return err
		}
		configuration.Replace[filepath.Join(base, relative)] = filepath.Join(directory, relative)
	}
	content, err := json.Marshal(configuration)
	if err != nil {
		return err
	}
	return root.WriteFile(discoveryOverlayFilename, content, 0o600)
}
