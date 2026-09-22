package command

import (
	"errors"
	"fmt"
	"go/format"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/console"
)

// MakeApp 创建原生业务应用骨架，发布构建由独立的 build 命令负责。
type MakeApp struct {
	console.Command
}

// Configure 配置应用构建命令。
func (command *MakeApp) Configure() {
	command.Signature = "make:app"
	command.Description = "Create application directories and components"
	command.AddArgument("app", "app name .", false)
}

// Execute 构建指定应用；省略名称时使用 default_app，默认值为 index。
func (command *MakeApp) Execute(input *console.Input, output *console.Output) error {
	if input == nil {
		return fmt.Errorf("命令输入不能为空")
	}
	if output == nil {
		return console.ErrInvalidOutput
	}
	if command == nil || command.App == nil {
		return framework.ErrNilApplication
	}
	name := strings.TrimSpace(input.GetArgument(0))
	if name == "" {
		name = defaultGeneratorApplication(command.App)
	}
	if err := validateNativeApplicationPackageName(name); err != nil {
		return fmt.Errorf("应用名不合法: %w", err)
	}
	if err := buildNativeApplication(command.App, name); err != nil {
		return fmt.Errorf("构建应用 %q 失败: %w", name, err)
	}
	output.Success("Successed")
	return nil
}

type buildGeneratedSnapshot struct {
	relativePath string
	content      []byte
	existed      bool
}

type applicationBuildTransaction struct {
	root               *os.Root
	createdFiles       []string
	createdDirectories []string
}

func buildNativeApplication(app *framework.App, name string) (returnErr error) {
	if app == nil {
		return framework.ErrNilApplication
	}
	if strings.TrimSpace(app.BasePath) == "" {
		return fmt.Errorf("项目根目录不能为空")
	}
	root, err := os.OpenRoot(app.BasePath)
	if err != nil {
		return fmt.Errorf("打开项目根目录失败: %w", err)
	}
	transaction := &applicationBuildTransaction{root: root}
	defer func() {
		returnErr = errors.Join(returnErr, root.Close())
	}()

	targetDirectory := filepath.Join("app", name)
	snapshots, err := transaction.captureGeneratedSnapshots(
		filepath.Join("app", applicationDiscoveryFilename),
		filepath.Join(targetDirectory, applicationDiscoveryFilename),
	)
	if err != nil {
		return err
	}
	if err := transaction.createApplicationDirectories(targetDirectory); err != nil {
		return errors.Join(err, transaction.rollback(snapshots))
	}
	sources := nativeApplicationBuildSources(name)
	sourcePaths := make([]string, 0, len(sources))
	for relativePath := range sources {
		sourcePaths = append(sourcePaths, relativePath)
	}
	sort.Strings(sourcePaths)
	for _, relativePath := range sourcePaths {
		source := sources[relativePath]
		created, err := transaction.createFile(relativePath, []byte(source), true)
		if err != nil {
			return errors.Join(err, transaction.rollback(snapshots))
		}
		if created {
			transaction.createdFiles = append(transaction.createdFiles, relativePath)
		}
	}
	viewPath := filepath.Join(targetDirectory, "view", "index.html")
	created, err := transaction.createFile(viewPath, []byte(nativeApplicationWelcomeView(name)), false)
	if err != nil {
		return errors.Join(err, transaction.rollback(snapshots))
	}
	if created {
		transaction.createdFiles = append(transaction.createdFiles, viewPath)
	}
	if err := RefreshControllerDiscovery(app); err != nil {
		return errors.Join(err, transaction.rollback(snapshots))
	}
	if err := CheckControllerDiscovery(app); err != nil {
		return errors.Join(err, transaction.rollback(snapshots))
	}
	return nil
}

func (transaction *applicationBuildTransaction) createApplicationDirectories(targetDirectory string) error {
	directories := []string{
		"app",
		targetDirectory,
		filepath.Join(targetDirectory, "config"),
		filepath.Join(targetDirectory, "controller"),
		filepath.Join(targetDirectory, "lang"),
		filepath.Join(targetDirectory, "middleware"),
		filepath.Join(targetDirectory, "model"),
		filepath.Join(targetDirectory, "route"),
		filepath.Join(targetDirectory, "validate"),
		filepath.Join(targetDirectory, "view"),
	}
	for _, directory := range directories {
		created, err := transaction.ensureDirectory(directory)
		if err != nil {
			return err
		}
		if created {
			transaction.createdDirectories = append(transaction.createdDirectories, directory)
		}
	}
	return nil
}

func (transaction *applicationBuildTransaction) ensureDirectory(relativePath string) (bool, error) {
	if transaction == nil || transaction.root == nil {
		return false, fmt.Errorf("应用构建事务未初始化")
	}
	information, err := transaction.root.Lstat(relativePath)
	if err == nil {
		if information.Mode()&os.ModeSymlink != 0 || !information.IsDir() {
			return false, fmt.Errorf("应用目录 %q 必须是真实目录", relativePath)
		}
		return false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("读取应用目录 %q 失败: %w", relativePath, err)
	}
	if err := transaction.root.Mkdir(relativePath, 0o755); err != nil {
		return false, fmt.Errorf("创建应用目录 %q 失败: %w", relativePath, err)
	}
	return true, nil
}

func (transaction *applicationBuildTransaction) createFile(relativePath string, content []byte, goSource bool) (bool, error) {
	if transaction == nil || transaction.root == nil {
		return false, fmt.Errorf("应用构建事务未初始化")
	}
	if information, err := transaction.root.Lstat(relativePath); err == nil {
		if !information.Mode().IsRegular() || information.Mode()&os.ModeSymlink != 0 {
			return false, fmt.Errorf("应用文件 %q 必须是普通文件", relativePath)
		}
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("读取应用文件 %q 失败: %w", relativePath, err)
	}
	if goSource {
		formatted, err := format.Source(content)
		if err != nil {
			return false, fmt.Errorf("格式化应用源码 %q 失败: %w", relativePath, err)
		}
		content = formatted
	}
	file, err := transaction.root.OpenFile(relativePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return false, fmt.Errorf("创建应用文件 %q 失败: %w", relativePath, err)
	}
	written, writeErr := file.Write(content)
	if writeErr == nil && written != len(content) {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil {
		return false, errors.Join(fmt.Errorf("写入应用文件 %q 失败: %w", relativePath, writeErr), file.Close(), transaction.root.Remove(relativePath))
	}
	if err := file.Sync(); err != nil {
		return false, errors.Join(fmt.Errorf("同步应用文件 %q 失败: %w", relativePath, err), file.Close(), transaction.root.Remove(relativePath))
	}
	if err := file.Close(); err != nil {
		return false, errors.Join(fmt.Errorf("关闭应用文件 %q 失败: %w", relativePath, err), transaction.root.Remove(relativePath))
	}
	return true, nil
}

func (transaction *applicationBuildTransaction) captureGeneratedSnapshots(relativePaths ...string) ([]buildGeneratedSnapshot, error) {
	snapshots := make([]buildGeneratedSnapshot, 0, len(relativePaths))
	for _, relativePath := range relativePaths {
		information, err := transaction.root.Lstat(relativePath)
		if errors.Is(err, os.ErrNotExist) {
			snapshots = append(snapshots, buildGeneratedSnapshot{relativePath: relativePath})
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("读取生成文件 %q 失败: %w", relativePath, err)
		}
		if !information.Mode().IsRegular() || information.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("生成文件 %q 必须是普通文件", relativePath)
		}
		content, err := transaction.root.ReadFile(relativePath)
		if err != nil {
			return nil, fmt.Errorf("备份生成文件 %q 失败: %w", relativePath, err)
		}
		snapshots = append(snapshots, buildGeneratedSnapshot{
			relativePath: relativePath,
			content:      content,
			existed:      true,
		})
	}
	return snapshots, nil
}

func (transaction *applicationBuildTransaction) rollback(snapshots []buildGeneratedSnapshot) error {
	if transaction == nil || transaction.root == nil {
		return nil
	}
	var rollbackErr error
	for _, snapshot := range snapshots {
		if snapshot.existed {
			rollbackErr = errors.Join(rollbackErr, replaceGeneratedSource(transaction.root.Name(), snapshot.relativePath, snapshot.content))
			continue
		}
		if err := transaction.root.Remove(snapshot.relativePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("回滚生成文件 %q 失败: %w", snapshot.relativePath, err))
		}
	}
	for index := len(transaction.createdFiles) - 1; index >= 0; index-- {
		if err := transaction.root.Remove(transaction.createdFiles[index]); err != nil && !errors.Is(err, os.ErrNotExist) {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("回滚应用文件 %q 失败: %w", transaction.createdFiles[index], err))
		}
	}
	for index := len(transaction.createdDirectories) - 1; index >= 0; index-- {
		if err := transaction.root.Remove(transaction.createdDirectories[index]); err != nil && !errors.Is(err, os.ErrNotExist) {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("回滚应用目录 %q 失败: %w", transaction.createdDirectories[index], err))
		}
	}
	return rollbackErr
}

func nativeApplicationBuildSources(name string) map[string]string {
	baseDirectory := filepath.Join("app", name)
	return map[string]string{
		filepath.Join(baseDirectory, "controller", "base_controller.go"): `package controller

import framework "github.com/zhuhanxin0308/thinkgo/v3"

// BaseController 是当前应用控制器的公共基础类型。
type BaseController struct {
	framework.Controller
}
`,
		filepath.Join(baseDirectory, "controller", "index.go"): `package controller

// Index 是当前应用的默认控制器。
type Index struct {
	BaseController
}

// Index 返回默认首页内容。
func (controller *Index) Index() string {
	return "ThinkGo"
}

// Hello 返回带名称的欢迎内容。
func (controller *Index) Hello(names ...string) string {
	name := "ThinkPHP"
	if len(names) > 0 && names[0] != "" {
		name = names[0]
	}
	return "hello," + name
}
`,
		filepath.Join(baseDirectory, "event.go"): `package ` + name + `

import framework "github.com/zhuhanxin0308/thinkgo/v3"

// Events 定义当前应用的事件监听关系。
func Events() framework.EventDefinition {
	return framework.EventDefinition{}
}
`,
		filepath.Join(baseDirectory, "middleware.go"): `package ` + name + `

import "github.com/zhuhanxin0308/thinkgo/v3/middleware"

// Middleware 返回当前应用中间件，执行顺序位于全局中间件之内。
func Middleware() []middleware.Handler {
	return nil
}
`,
		filepath.Join(baseDirectory, "provider.go"): `package ` + name + `

// Providers 返回当前应用的容器绑定。
func Providers() map[string]interface{} {
	return map[string]interface{}{}
}
`,
		filepath.Join(baseDirectory, "service.go"): `package ` + name + `

// Services 返回当前应用独占的服务定义。
func Services() []interface{} {
	return nil
}
`,
		filepath.Join(baseDirectory, "route", "app.go"): `package route

import framework "github.com/zhuhanxin0308/thinkgo/v3"

// Load 注册当前应用路由。
func Load(route *framework.Route) {
	route.Get("/", "Index@Index")
	route.Get("hello/:name", "Index@Hello")
}
`,
	}
}

func nativeApplicationWelcomeView(name string) string {
	return "<!doctype html>\n<html lang=\"zh-CN\">\n<head>\n    <meta charset=\"utf-8\">\n    <title>" + name + " - ThinkGo</title>\n</head>\n<body>\n    <h1>ThinkGo</h1>\n</body>\n</html>\n"
}
