package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/config"
	"github.com/zhuhanxin0308/thinkgo/v3/console"
	frameworkContext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/db"
	frameworkRoute "github.com/zhuhanxin0308/thinkgo/v3/route"
)

// Optimize 构建后续应用进程实际消费的启动缓存。
type Optimize struct {
	console.Command
}

// Configure 配置 optimize 命令。
func (command *Optimize) Configure() {
	command.Signature = "optimize"
	command.Description = "Build application config, route and schema caches."
}

// Execute 保留执行上下文，依次构建应用配置、命名路由和数据库字段缓存。
func (command *Optimize) Execute(input *console.Input, output *console.Output) error {
	if command == nil || command.App == nil {
		return framework.ErrNilApplication
	}
	if output == nil {
		return console.ErrInvalidOutput
	}
	for _, operation := range []console.ICommand{
		&OptimizeConfig{Command: console.Command{App: command.App}},
		&OptimizeRoute{Command: console.Command{App: command.App}},
		&OptimizeSchema{Command: console.Command{App: command.App}},
	} {
		if err := operation.Execute(input, output); err != nil {
			return err
		}
	}
	return output.Err()
}

// OptimizeConfig 构建下一次应用启动会优先读取的配置缓存。
type OptimizeConfig struct {
	console.Command
}

// Configure 配置 optimize:config [dir] 命令。
func (command *OptimizeConfig) Configure() {
	command.Signature = "optimize:config"
	command.Description = "Build config cache."
	command.AddArgument("dir", "dir name .", false)
}

// Execute 将最终配置快照写入 runtime/config.json。
func (command *OptimizeConfig) Execute(input *console.Input, output *console.Output) error {
	if command == nil || command.App == nil {
		return framework.ErrNilApplication
	}
	if output == nil {
		return console.ErrInvalidOutput
	}
	directory, err := optimizeDirectory(input)
	if err != nil {
		return err
	}
	directories := []string{directory}
	if directory == "" {
		directories, err = nativeOptimizationDirectories(command.App, "config", true)
		if err != nil {
			return err
		}
	}
	for _, currentDirectory := range directories {
		snapshot, err := optimizedConfigSnapshot(command.App, currentDirectory)
		if err != nil {
			return err
		}
		content, err := json.MarshalIndent(snapshot, "", "    ")
		if err != nil {
			return fmt.Errorf("序列化配置缓存失败: %w", err)
		}
		content = append(content, '\n')
		target := filepath.Join(command.App.GetRootPath(), "runtime", currentDirectory, "config.json")
		if err := writeProjectFileAtomically(command.App, target, content); err != nil {
			return fmt.Errorf("写入应用 %q 配置缓存失败: %w", currentDirectory, err)
		}
	}
	output.Info("Succeed!")
	return output.Err()
}

func optimizedConfigSnapshot(app *framework.App, directory string) (map[string]interface{}, error) {
	configuration, err := resolveApplicationConfig(app)
	if err != nil {
		return nil, fmt.Errorf("应用配置不可用: %w", err)
	}
	snapshot := app.ProjectConfig()
	if len(snapshot) == 0 {
		snapshot = configuration.GetMap("")
	}
	if directory == "" {
		return snapshot, nil
	}
	working := config.NewConfig()
	keys := make([]string, 0, len(snapshot))
	for key := range snapshot {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := working.Set(key, snapshot[key]); err != nil {
			return nil, fmt.Errorf("复制配置命名空间 %q 失败: %w", key, err)
		}
	}
	path := filepath.Join(app.GetBasePath(), directory, "config")
	if err := working.LoadAll(path); err != nil {
		return nil, fmt.Errorf("%s directory does not exist or cannot be loaded: %w", path, err)
	}
	if err := working.ApplyEnvironment(app.Env()); err != nil {
		return nil, fmt.Errorf("合并环境配置失败: %w", err)
	}
	return working.GetMap(""), nil
}

// RouteExport 加载实际路由并导出稳定清单，供检查和部署产物比较使用。
type RouteExport struct {
	console.Command
}

// Configure 配置 route:export [dir] 命令。
func (command *RouteExport) Configure() {
	command.Signature = "route:export"
	command.Description = "Export registered application routes."
	command.AddArgument("dir", "dir name .", false)
}

// Execute 校验全部已编译路由后写入 runtime/route.json。
func (command *RouteExport) Execute(input *console.Input, output *console.Output) error {
	if command == nil || command.App == nil {
		return framework.ErrNilApplication
	}
	if output == nil {
		return console.ErrInvalidOutput
	}
	directory, err := optimizeDirectory(input)
	if err != nil {
		return err
	}
	applications, err := compiledApplicationsForOptimization(command.App, directory, "route")
	if err != nil {
		return err
	}
	for _, application := range applications {
		if err := writeApplicationRouteCache(command.App, application); err != nil {
			return err
		}
	}
	output.Info("Succeed!")
	return output.Err()
}

func writeApplicationRouteCache(project, application *framework.App) error {
	if application == nil {
		return framework.ErrNilApplication
	}
	if err := application.LoadRoutes(); err != nil {
		return fmt.Errorf("加载应用 %q 路由失败: %w", application.CurrentApplicationName(), err)
	}
	routes, err := application.Route().Routes()
	if err != nil {
		return fmt.Errorf("构建应用 %q 路由索引失败: %w", application.CurrentApplicationName(), err)
	}
	manifest, err := buildRouteCacheManifest(routes)
	if err != nil {
		return err
	}
	content, err := json.MarshalIndent(manifest, "", "    ")
	if err != nil {
		return fmt.Errorf("序列化应用 %q 路由清单失败: %w", application.CurrentApplicationName(), err)
	}
	content = append(content, '\n')
	target := filepath.Join(application.GetRuntimePath(), "route.json")
	if err := writeProjectFileAtomically(project, target, content); err != nil {
		return fmt.Errorf("写入应用 %q 路由清单失败: %w", application.CurrentApplicationName(), err)
	}
	return nil
}

type routeCacheEntry struct {
	Method            string `json:"method"`
	Path              string `json:"path"`
	Handler           string `json:"handler"`
	Name              string `json:"name,omitempty"`
	Domain            string `json:"domain,omitempty"`
	Extension         string `json:"extension,omitempty"`
	ExtensionRequired bool   `json:"extension_required"`
	Auto              bool   `json:"auto"`
	MiddlewareCount   int    `json:"middleware_count"`
}

func buildRouteCacheManifest(routes []frameworkRoute.RouteInfo) ([]routeCacheEntry, error) {
	manifest := make([]routeCacheEntry, 0, len(routes))
	for _, registered := range routes {
		handler, err := routeHandlerIdentity(registered.Handler)
		if err != nil {
			return nil, fmt.Errorf("路由 %s %s 的处理器无法导出: %w", registered.Method, registered.Path, err)
		}
		manifest = append(manifest, routeCacheEntry{
			Method:            registered.Method,
			Path:              registered.Path,
			Handler:           handler,
			Name:              registered.Name,
			Domain:            registered.Domain,
			Extension:         registered.Extension,
			ExtensionRequired: registered.ExtensionRequired,
			Auto:              registered.Auto,
			MiddlewareCount:   registered.MiddlewareCount,
		})
	}
	return manifest, nil
}

func routeHandlerIdentity(handler frameworkRoute.HandlerFunc) (string, error) {
	switch typed := handler.(type) {
	case *frameworkRoute.JSONHandler:
		if typed == nil {
			return "", frameworkRoute.ErrInvalidRouteHandler
		}
		return routeHandlerIdentity(typed.Callback())
	case string:
		return typed, nil
	case func(*frameworkContext.Request) *frameworkContext.Response:
		return functionIdentity(typed), nil
	case func(http.ResponseWriter, *http.Request):
		return functionIdentity(typed), nil
	case http.Handler:
		return reflect.TypeOf(typed).String(), nil
	default:
		value := reflect.ValueOf(handler)
		if value.IsValid() && value.Kind() == reflect.Func && !value.IsNil() {
			return functionIdentity(handler), nil
		}
		return "", fmt.Errorf("未知处理器类型 %T", handler)
	}
}

func functionIdentity(function interface{}) string {
	value := reflect.ValueOf(function)
	if !value.IsValid() || value.Kind() != reflect.Func || value.IsNil() {
		return "<Closure>"
	}
	if description := runtime.FuncForPC(value.Pointer()); description != nil {
		return description.Name()
	}
	return "<Closure>"
}

// SchemaValidate 校验自动发现模型的映射定义，或验证显式指定的数据表可以查询。
type SchemaValidate struct {
	console.Command
}

// Configure 配置 schema:validate [dir] [--connection] [--table] 命令。
func (command *SchemaValidate) Configure() {
	command.Signature = "schema:validate"
	command.Description = "Validate model mappings or table accessibility."
	command.AddArgument("dir", "dir name .", false)
	command.AddOption("connection", "", "connection name .", "")
	command.AddOption("table", "", "table name .", "")
}

// Execute 对模型类型或指定数据表执行校验，不宣称 CLI 内存缓存能留给其他进程使用。
func (command *SchemaValidate) Execute(input *console.Input, output *console.Output) error {
	if command == nil || command.App == nil {
		return framework.ErrNilApplication
	}
	if output == nil {
		return console.ErrInvalidOutput
	}
	directory, err := optimizeDirectory(input)
	if err != nil {
		return err
	}
	table := ""
	connection := ""
	if input != nil {
		table = strings.TrimSpace(input.GetOption("table"))
		connection = strings.TrimSpace(input.GetOption("connection"))
	}
	if table != "" {
		if err := validateTableAccess(input.Context(), command.App, connection, table); err != nil {
			return err
		}
	} else {
		applications, err := compiledApplicationsForOptimization(command.App, directory, "model")
		if err != nil {
			return err
		}
		for _, application := range applications {
			if err := db.PrewarmModelMetadata(application.RegisteredModelTypes()...); err != nil {
				return fmt.Errorf("校验应用 %q 模型失败: %w", application.CurrentApplicationName(), err)
			}
		}
	}
	output.Info("Succeed!")
	return output.Err()
}

func nativeOptimizationDirectories(app *framework.App, component string, includeRoot bool) ([]string, error) {
	directories := make([]string, 0, len(app.ApplicationNames())+1)
	if includeRoot {
		directories = append(directories, "")
	}
	for _, name := range app.ApplicationNames() {
		path := filepath.Join(app.GetBasePath(), name, component)
		information, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("读取应用组件 %q 失败: %w", path, err)
		}
		if !information.IsDir() {
			return nil, fmt.Errorf("应用组件 %q 必须是目录", path)
		}
		directories = append(directories, name)
	}
	return directories, nil
}

func compiledApplicationsForOptimization(project *framework.App, directory, component string) ([]*framework.App, error) {
	if project == nil {
		return nil, framework.ErrNilApplication
	}
	names := project.ApplicationNames()
	if len(names) == 0 {
		if directory != "" {
			return nil, fmt.Errorf("%s directory cannot be loaded by the current compiled application", filepath.Join(project.GetBasePath(), directory, component))
		}
		return []*framework.App{project}, nil
	}
	applications, err := project.BuildApplications()
	if err != nil {
		return nil, fmt.Errorf("构建原生应用清单失败: %w", err)
	}
	selectedNames := names
	if directory != "" {
		selectedNames = []string{directory}
	}
	selected := make([]*framework.App, 0, len(selectedNames))
	for _, name := range selectedNames {
		application := applications[name]
		componentPath := filepath.Join(project.GetBasePath(), name, component)
		information, statErr := os.Stat(componentPath)
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return nil, fmt.Errorf("读取应用组件 %q 失败: %w", componentPath, statErr)
		}
		if statErr == nil && !information.IsDir() {
			return nil, fmt.Errorf("应用组件 %q 必须是目录", componentPath)
		}
		if application == nil || statErr != nil || !information.IsDir() {
			if directory != "" {
				return nil, fmt.Errorf("%s directory does not exist or is not compiled", componentPath)
			}
			continue
		}
		if !application.Initialized() {
			if err := application.Initialize(); err != nil {
				return nil, fmt.Errorf("初始化应用 %q 失败: %w", name, err)
			}
		}
		if err := application.BootProviders(); err != nil {
			return nil, fmt.Errorf("启动应用 %q 服务失败: %w", name, err)
		}
		selected = append(selected, application)
	}
	return selected, nil
}

func validateTableAccess(ctx context.Context, app *framework.App, connectionName, table string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	manager := app.DBManager()
	if manager == nil {
		return db.ErrDatabaseUnavailable
	}
	var database *db.DB
	var err error
	if connectionName == "" {
		database, err = manager.Default()
	} else {
		database, err = manager.Connection(connectionName)
	}
	if err != nil {
		return fmt.Errorf("连接数据库失败: %w", err)
	}
	if table == "*" {
		return fmt.Errorf("当前数据库驱动不能在不指定数据库名时安全枚举全部表")
	}
	if _, err := database.Table(table).WithContext(ctx).Limit(1).Select(); err != nil {
		return fmt.Errorf("验证数据表 %q 可查询性失败: %w", table, err)
	}
	return nil
}

func optimizeDirectory(input *console.Input) (string, error) {
	if input == nil {
		return "", nil
	}
	return safeOptionalDirectory(input.GetArgument(0))
}
