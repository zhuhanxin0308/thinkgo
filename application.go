package framework

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/zhuhanxin0308/thinkgo/framework/middleware"
)

var (
	// ErrInvalidApplicationDefinition 表示编译发现的应用名称或加载器不合法。
	ErrInvalidApplicationDefinition = errors.New("无效应用定义")
	// ErrDuplicateApplication 表示同一项目重复声明了同名应用。
	ErrDuplicateApplication = errors.New("应用重复定义")
	// ErrNoApplications 表示原生多应用项目没有任何业务应用。
	ErrNoApplications = errors.New("没有可用应用")
)

// ApplicationDefinition 是 service:discover 生成代码与框架之间的编译期契约。
// 业务开发者不需要手工创建或维护该结构。
type ApplicationDefinition struct {
	Name     string
	Register ApplicationLoader
}

// applicationCatalog 保存一个项目的稳定应用清单和由根 App 拥有的实例。
// 它不保存请求级“当前应用”，请求选择始终由 HTTP 内核完成。
type applicationCatalog struct {
	lock         sync.Mutex
	globalLoader ApplicationLoader
	definitions  map[string]ApplicationDefinition
	names        []string
	primary      string
	applications map[string]*App
	paths        map[string]string
	buildErr     error
	built        bool
}

// RegisterApplications 注册 service:discover 生成的原生多应用清单。
// globalLoader 对应根 app 下的 service、event、middleware 和 provider，
// definitions 对应 app/<name> 中的业务组件。
func (app *App) RegisterApplications(globalLoader ApplicationLoader, definitions ...ApplicationDefinition) error {
	if app == nil {
		return ErrNilApplication
	}
	if globalLoader == nil {
		return fmt.Errorf("%w: 全局应用加载器不能为空", ErrInvalidApplicationDefinition)
	}
	if len(definitions) == 0 {
		return ErrNoApplications
	}

	normalized := make(map[string]ApplicationDefinition, len(definitions))
	names := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		name, err := normalizeApplicationName(definition.Name)
		if err != nil {
			return err
		}
		if definition.Register == nil {
			return fmt.Errorf("%w: 应用 %q 的加载器不能为空", ErrInvalidApplicationDefinition, name)
		}
		if _, exists := normalized[name]; exists {
			return fmt.Errorf("%w: %q", ErrDuplicateApplication, name)
		}
		definition.Name = name
		normalized[name] = definition
		names = append(names, name)
	}
	sort.Strings(names)
	primary := names[0]
	if _, exists := normalized["index"]; exists {
		primary = "index"
	}

	app.registrationMu.Lock()
	defer app.registrationMu.Unlock()
	if err := app.ensureRegistrationOpen(); err != nil {
		return err
	}
	if app.applicationCatalog != nil {
		return fmt.Errorf("%w: 原生应用清单", ErrDuplicateRegistration)
	}
	if err := app.registry.registerApplicationLoader(globalLoader); err != nil {
		return err
	}
	app.applicationCatalog = &applicationCatalog{
		globalLoader: globalLoader,
		definitions:  normalized,
		names:        append([]string(nil), names...),
		primary:      primary,
		paths:        make(map[string]string),
	}
	app.applicationName = primary
	app.applicationDefinition = normalized[primary]
	return nil
}

// CurrentApplicationName 返回该 App 实例承载的业务应用名称。
// HTTP 请求中的当前应用仍以请求上下文为准，避免并发请求相互污染。
func (app *App) CurrentApplicationName() string {
	if app == nil {
		return ""
	}
	app.metadataMu.RLock()
	name := app.applicationName
	app.metadataMu.RUnlock()
	return name
}

// ApplicationMiddleware 返回当前业务应用的中间件管道。
// HTTP 内核用它保持全局中间件与 app/<name>/middleware.go 的嵌套顺序。
func (app *App) ApplicationMiddleware() *middleware.Pipeline {
	if app == nil {
		return nil
	}
	return app.applicationMiddleware
}

// ApplicationNames 返回 service:discover 发现的应用名称稳定快照。
func (app *App) ApplicationNames() []string {
	if app == nil || app.applicationCatalog == nil {
		return nil
	}
	catalog := app.applicationCatalog
	catalog.lock.Lock()
	names := append([]string(nil), catalog.names...)
	catalog.lock.Unlock()
	return names
}

// ConfigureApplicationPath 为 Http.Name().Path() 绑定已编译应用的自定义目录。
// 普通项目无需调用；未设置时始终使用 app/<name>。
func (app *App) ConfigureApplicationPath(name, path string) error {
	if app == nil {
		return ErrNilApplication
	}
	name, err := normalizeApplicationName(name)
	if err != nil {
		return err
	}
	normalizedPath, err := app.normalizeProjectPath(path, "应用目录")
	if err != nil {
		return err
	}
	catalog := app.applicationCatalog
	if catalog == nil {
		return ErrNoApplications
	}
	initialized := app.Initialized()
	catalog.lock.Lock()
	defer catalog.lock.Unlock()
	if _, exists := catalog.definitions[name]; !exists {
		return fmt.Errorf("%w: 应用 %q 未编译", ErrInvalidApplicationDefinition, name)
	}
	if catalog.built || name == catalog.primary && initialized {
		return fmt.Errorf("%w: 应用 %q 已经初始化", ErrApplicationRunning, name)
	}
	catalog.paths[name] = normalizedPath
	return nil
}

// BuildApplications 为每个业务应用创建独立且并发安全的 App 实例。
// index（存在时）复用入口 App，其余实例由入口 App 统一负责关闭。
func (app *App) BuildApplications() (map[string]*App, error) {
	if app == nil {
		return nil, ErrNilApplication
	}
	catalog := app.applicationCatalog
	if catalog == nil {
		return map[string]*App{"index": app}, nil
	}

	catalog.lock.Lock()
	defer catalog.lock.Unlock()
	if catalog.built {
		return cloneApplicationMap(catalog.applications), catalog.buildErr
	}
	catalog.built = true
	applications := make(map[string]*App, len(catalog.names))
	for _, name := range catalog.names {
		definition := catalog.definitions[name]
		applicationPath := filepath.Join(app.BasePath, "app", name)
		if configured := catalog.paths[name]; configured != "" {
			applicationPath = configured
		}
		if err := validateApplicationDirectory(applicationPath, name); err != nil {
			catalog.buildErr = errors.Join(catalog.buildErr, err)
			continue
		}
		if name == catalog.primary {
			app.nativeApplicationPath = applicationPath
			applications[name] = app
			continue
		}
		child := newAppWithOptions(app.skipDatabaseInit, app.BasePath)
		copyApplicationConstructionOptions(child, app)
		child.applicationName = name
		child.applicationDefinition = definition
		child.nativeApplicationPath = applicationPath
		if err := child.RegisterApplicationLoader(catalog.globalLoader); err != nil {
			catalog.buildErr = errors.Join(catalog.buildErr, fmt.Errorf("注册应用 %q 全局加载器失败: %w", name, err))
			_ = child.Close()
			continue
		}
		applications[name] = child
	}
	if len(applications) != len(catalog.names) && catalog.buildErr == nil {
		catalog.buildErr = ErrNoApplications
	}
	catalog.applications = applications
	return cloneApplicationMap(applications), catalog.buildErr
}

func normalizeApplicationName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > maxRegistrationNameBytes || name == "." || name == ".." {
		return "", fmt.Errorf("%w: 应用名称 %q 非法", ErrInvalidApplicationDefinition, name)
	}
	for _, character := range name {
		if unicode.IsControl(character) || (!unicode.IsLetter(character) && !unicode.IsDigit(character) && character != '_' && character != '-') {
			return "", fmt.Errorf("%w: 应用名称 %q 包含非法字符", ErrInvalidApplicationDefinition, name)
		}
	}
	return name, nil
}

func validateApplicationDirectory(path, name string) error {
	information, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("%w: 应用 %q 目录不可用: %w", ErrInvalidApplicationDefinition, name, err)
	}
	if information.Mode()&os.ModeSymlink != 0 || !information.IsDir() {
		return fmt.Errorf("%w: 应用 %q 必须是普通目录", ErrInvalidApplicationDefinition, name)
	}
	return nil
}

func copyApplicationConstructionOptions(target, source *App) {
	if target == nil || source == nil {
		return
	}
	projectConfiguration := source.ensureProjectConfigurationState()
	source.metadataMu.RLock()
	target.baseEnvName = source.baseEnvName
	target.envName = source.envName
	target.configExt = source.configExt
	target.consoleMode = source.consoleMode
	target.nativeRuntimeBasePath = source.nativeRuntimeBasePath
	source.metadataMu.RUnlock()
	target.projectConfiguration = projectConfiguration
}

func cloneApplicationMap(applications map[string]*App) map[string]*App {
	result := make(map[string]*App, len(applications))
	for name, application := range applications {
		result[name] = application
	}
	return result
}

func (app *App) hasNativeApplication() bool {
	return app != nil && app.applicationDefinition.Name != "" && app.applicationDefinition.Register != nil
}

// ProjectApplicationConfig 返回加载具体 app/<name>/config 之前的 app 配置快照。
// HTTP 内核用它保持 default_app、app_map、domain_bind 与 deny_app_list 的
// 项目级解析语义，避免某个业务应用的配置污染其它并发请求。
func (app *App) ProjectApplicationConfig() map[string]interface{} {
	if app == nil {
		return nil
	}
	app.metadataMu.RLock()
	snapshot := app.projectConfigSnapshot
	app.metadataMu.RUnlock()
	if snapshot == nil {
		if app.config == nil {
			return nil
		}
		return app.config.GetMap("app")
	}
	return snapshot.GetMap("app")
}

// ProjectConfig 返回加载 app/<name>/config 之前的完整项目配置快照。
// 多应用控制台命令用它分别叠加各应用配置，避免从当前默认应用继续克隆。
func (app *App) ProjectConfig() map[string]interface{} {
	if app == nil {
		return nil
	}
	app.metadataMu.RLock()
	snapshot := app.projectConfigSnapshot
	app.metadataMu.RUnlock()
	if snapshot == nil {
		if app.config == nil {
			return nil
		}
		return app.config.GetMap("")
	}
	return snapshot.GetMap("")
}

func (app *App) captureProjectApplicationConfig() error {
	if app == nil {
		return ErrNilApplication
	}
	if app.config == nil {
		return fmt.Errorf("捕获项目应用配置失败: 配置服务不能为空")
	}
	// 项目解析基线与全局应用加载器产生的配置都以同一递归快照语义固化。
	snapshot := app.config.Clone()
	app.metadataMu.Lock()
	app.projectConfigSnapshot = snapshot
	app.metadataMu.Unlock()
	return nil
}

// loadNativeApplication 在全局 AppInit 后叠加当前应用目录、配置和业务组件。
func (app *App) loadNativeApplication() error {
	if !app.hasNativeApplication() {
		return nil
	}
	name := app.CurrentApplicationName()
	applicationPath := app.configuredNativeApplicationPath(name)
	if err := app.setAppPath(applicationPath, false); err != nil {
		return fmt.Errorf("设置应用 %q 目录失败: %w", name, err)
	}
	runtimeBasePath := app.nativeRuntimeBasePath
	if runtimeBasePath == "" {
		runtimeBasePath = filepath.Join(app.GetRootPath(), "runtime")
	}
	if err := app.setRuntimePath(filepath.Join(runtimeBasePath, name), false); err != nil {
		return fmt.Errorf("设置应用 %q 运行目录失败: %w", name, err)
	}
	namespace := strings.TrimSpace(app.config.GetString("app.app_namespace"))
	if namespace == "" {
		namespace = "app\\" + name
	}
	app.SetNamespace(namespace)

	configPath := filepath.Join(applicationPath, "config")
	if information, err := os.Stat(configPath); err == nil && information.IsDir() {
		if err := app.config.LoadAll(configPath); err != nil {
			return fmt.Errorf("加载应用 %q 配置失败: %w", name, err)
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("读取应用 %q 配置目录失败: %w", name, err)
	}
	// 应用目录可能声明项目配置中不存在的新叶子；加载覆盖层后重新应用环境，
	// 保证真实环境与 .env 始终保持最高优先级。
	if err := app.config.ApplyEnvironment(app.env); err != nil {
		return fmt.Errorf("合并应用 %q 环境配置失败: %w", name, err)
	}
	app.registrationMu.Lock()
	app.loadingNativeDefinition = true
	app.registrationMu.Unlock()
	loadErr := func() error {
		defer func() {
			app.registrationMu.Lock()
			app.loadingNativeDefinition = false
			app.registrationMu.Unlock()
		}()
		return safeLoadApplication(app.applicationDefinition.Register, app)
	}()
	if loadErr != nil {
		return fmt.Errorf("加载应用 %q 定义失败: %w", name, loadErr)
	}
	return nil
}

func (app *App) configuredNativeApplicationPath(name string) string {
	if app == nil {
		return ""
	}
	if app.nativeApplicationPath != "" {
		return app.nativeApplicationPath
	}
	if catalog := app.applicationCatalog; catalog != nil {
		catalog.lock.Lock()
		configured := catalog.paths[name]
		catalog.lock.Unlock()
		if configured != "" {
			return configured
		}
	}
	return filepath.Join(app.GetBasePath(), name)
}

// closeOwnedApplications 由入口 App 逆序关闭其余应用实例。
func (app *App) closeOwnedApplications() error {
	if app == nil || app.applicationCatalog == nil {
		return nil
	}
	catalog := app.applicationCatalog
	catalog.lock.Lock()
	applications := cloneApplicationMap(catalog.applications)
	names := append([]string(nil), catalog.names...)
	catalog.lock.Unlock()
	var closeErr error
	for index := len(names) - 1; index >= 0; index-- {
		current := applications[names[index]]
		if current == nil || current == app {
			continue
		}
		if err := current.Close(); err != nil {
			closeErr = errors.Join(closeErr, fmt.Errorf("关闭应用 %q 失败: %w", names[index], err))
		}
	}
	return closeErr
}
