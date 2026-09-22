package command

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/console"
)

const applicationDiscoveryFilename = "autoload_generated.go"

// ServiceDiscover 刷新 Go 静态编译所需的控制器发现文件。
// 命令名与 ThinkPHP 的 service:discover 保持一致。
type ServiceDiscover struct {
	console.Command
}

// Configure 配置服务发现命令。
func (command *ServiceDiscover) Configure() {
	command.Signature = "service:discover"
	command.Description = "Discover Services for ThinkPHP"
}

// Execute 扫描全部原生应用目录并生成稳定注册代码。
func (command *ServiceDiscover) Execute(input *console.Input, output *console.Output) error {
	if command == nil || command.App == nil {
		return framework.ErrNilApplication
	}
	if output == nil {
		return console.ErrInvalidOutput
	}
	err := RefreshControllerDiscovery(command.App)
	if err != nil {
		return fmt.Errorf("刷新控制器发现信息失败: %w", err)
	}
	output.Success("Succeed!")
	return nil
}

// RefreshControllerDiscovery 根据 app/<应用名> 生成各应用装配入口与根应用清单；
// 保留函数名以兼容已有构建调用。
func RefreshControllerDiscovery(app *framework.App) error {
	controllerDiscoveryTransactionMu.Lock()
	defer controllerDiscoveryTransactionMu.Unlock()
	sources, err := nativeApplicationDiscoverySources(app)
	if err != nil {
		return err
	}
	return replaceGeneratedSourceBatch(app.BasePath, sources)
}

// CheckControllerDiscovery 校验全部发现文件与原生应用源码一致，不修改工作区。
func CheckControllerDiscovery(app *framework.App) error {
	controllerDiscoveryTransactionMu.Lock()
	defer controllerDiscoveryTransactionMu.Unlock()
	sources, err := nativeApplicationDiscoverySources(app)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(app.BasePath)
	if err != nil {
		return fmt.Errorf("打开项目根目录失败: %w", err)
	}
	defer root.Close()
	for _, generated := range sources {
		current, err := root.ReadFile(generated.relativePath)
		if err != nil {
			return fmt.Errorf("读取控制器发现文件 %q 失败: %w", generated.relativePath, err)
		}
		if !bytes.Equal(current, generated.source) {
			return fmt.Errorf("控制器发现文件 %q 已过期，请执行 service:discover", generated.relativePath)
		}
	}
	return nil
}

// excludeApplicationType 排除 ThinkPHP 应用目录中的标准基础类型，避免把
// BaseController 等扩展点误注册成可调度业务控制器。
func excludeApplicationType(typeNames []string, excluded string) []string {
	filtered := make([]string, 0, len(typeNames))
	for _, name := range typeNames {
		if name != excluded {
			filtered = append(filtered, name)
		}
	}
	return filtered
}

func discoverApplicationStructTypes(basePath, directory, packageName string) ([]string, bool, error) {
	context, err := newApplicationSemanticContextFromModule(basePath)
	if err != nil {
		return nil, false, err
	}
	return discoverApplicationStructTypesWithContext(context, directory, packageName)
}

func applicationPackageExists(basePath, directory, packageName string) (bool, error) {
	context, err := newApplicationSemanticContextFromModule(basePath)
	if err != nil {
		return false, err
	}
	return applicationPackageExistsWithContext(context, directory, packageName)
}

func discoverApplicationConstructors(basePath, directory string, typeNames []string) (map[string]bool, error) {
	context, err := newApplicationSemanticContextFromModule(basePath)
	if err != nil {
		return nil, err
	}
	return discoverApplicationConstructorsWithContext(context, directory, filepath.Base(directory), typeNames)
}

func readApplicationModulePath(basePath string) (string, error) {
	root, err := os.OpenRoot(basePath)
	if err != nil {
		return "", fmt.Errorf("打开项目根目录失败: %w", err)
	}
	defer root.Close()
	content, err := root.ReadFile("go.mod")
	if err != nil {
		return "", fmt.Errorf("读取 go.mod 失败: %w", err)
	}
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "module" && fields[1] != "" && !strings.ContainsAny(fields[1], "\"'`\x00\r\n") {
			return fields[1], nil
		}
	}
	return "", fmt.Errorf("go.mod 缺少合法 module 声明")
}

func replaceGeneratedSource(basePath, relativePath string, source []byte) (returnErr error) {
	root, err := os.OpenRoot(basePath)
	if err != nil {
		return fmt.Errorf("打开项目根目录失败: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, root.Close())
	}()
	if current, readErr := root.ReadFile(relativePath); readErr == nil && bytes.Equal(current, source) {
		return nil
	}
	if err := root.MkdirAll(filepath.Dir(relativePath), 0o755); err != nil {
		return fmt.Errorf("创建控制器发现目录失败: %w", err)
	}
	permission := os.FileMode(0o644)
	if information, statErr := root.Stat(relativePath); statErr == nil {
		permission = information.Mode().Perm()
	}
	temporaryPath := fmt.Sprintf("%s.tmp-%d", relativePath, time.Now().UnixNano())
	file, err := root.OpenFile(temporaryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, permission)
	if err != nil {
		return fmt.Errorf("创建控制器发现临时文件失败: %w", err)
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			returnErr = errors.Join(returnErr, root.Remove(temporaryPath))
		}
	}()
	written, writeErr := file.Write(source)
	if writeErr == nil && written != len(source) {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil {
		return errors.Join(fmt.Errorf("写入控制器发现临时文件失败: %w", writeErr), file.Close())
	}
	if err := file.Sync(); err != nil {
		return errors.Join(fmt.Errorf("同步控制器发现临时文件失败: %w", err), file.Close())
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("关闭控制器发现临时文件失败: %w", err)
	}
	if err := root.Rename(temporaryPath, relativePath); err != nil {
		return fmt.Errorf("替换控制器发现文件失败: %w", err)
	}
	removeTemporary = false
	return nil
}
