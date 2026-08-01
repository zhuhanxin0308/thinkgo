package framework

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

var (
	// ErrNoApplications 表示宿主没有发现任何应用定义。
	ErrNoApplications = errors.New("没有可用应用")
	// ErrApplicationManagerClosed 表示应用管理器已经关闭。
	ErrApplicationManagerClosed = errors.New("应用管理器已经关闭")
	// ErrApplicationManagerRunning 表示应用管理器正在运行。
	ErrApplicationManagerRunning = errors.New("应用管理器正在运行")
	// ErrApplicationManagerOwnsLifecycle 表示应用生命周期必须由所属管理器统一管理。
	ErrApplicationManagerOwnsLifecycle = errors.New("应用生命周期由管理器负责")
	// ErrConflictingDefaultApplication 表示多个应用声明了互相冲突的默认应用。
	ErrConflictingDefaultApplication = errors.New("应用默认应用配置冲突")
)

// ApplicationManager 管理同一进程内的多个独立 App 实例。
// 管理器只负责应用生命周期，不保存任何请求级当前应用状态。
type ApplicationManager struct {
	basePath         string
	definitions      []ApplicationDefinition
	applicationNames []string
	applications     map[string]*App
	defaultAppName   string

	lock       sync.Mutex
	booting    bool
	booted     bool
	bootErr    error
	closed     bool
	closing    bool
	closeDone  chan struct{}
	closeErr   error
	runPending bool
	running    bool

	applicationResolverMu sync.Mutex
	applicationResolver   *ApplicationResolver
}

// NewApplicationManager 根据包级应用定义创建多应用管理器。
func NewApplicationManager(basePath string) (*ApplicationManager, error) {
	return newApplicationManager(basePath, ApplicationDefinitions(), false)
}

// NewApplicationManagerFromDefinitions 根据显式应用定义创建管理器，便于嵌入式宿主和测试隔离全局注册表。
func NewApplicationManagerFromDefinitions(basePath string, definitions []ApplicationDefinition, skipDatabase bool) (*ApplicationManager, error) {
	return newApplicationManager(basePath, definitions, skipDatabase)
}

// BuildApplicationManager 使用严格构建语义创建多应用管理器，初始化失败时返回 nil 和错误。
func BuildApplicationManager(basePath string) (*ApplicationManager, error) {
	return newApplicationManagerWithOptions(basePath, ApplicationDefinitions(), false, applicationManagerConstructionOptions{
		failOnInitialization: true,
	})
}

// BuildApplicationManagerFromDefinitions 使用严格构建语义创建显式定义的多应用管理器。
func BuildApplicationManagerFromDefinitions(basePath string, definitions []ApplicationDefinition, skipDatabase bool) (*ApplicationManager, error) {
	return newApplicationManagerWithOptions(basePath, definitions, skipDatabase, applicationManagerConstructionOptions{
		failOnInitialization: true,
	})
}

type applicationManagerConstructionOptions struct {
	failOnInitialization bool
}

func newApplicationManager(basePath string, definitions []ApplicationDefinition, skipDatabase bool) (*ApplicationManager, error) {
	return newApplicationManagerWithOptions(basePath, definitions, skipDatabase, applicationManagerConstructionOptions{})
}

func newApplicationManagerWithOptions(basePath string, definitions []ApplicationDefinition, skipDatabase bool, options applicationManagerConstructionOptions) (*ApplicationManager, error) {
	rootPath, err := normalizeManagerBasePath(basePath)
	if err != nil {
		return nil, err
	}
	if len(definitions) == 0 {
		return nil, ErrNoApplications
	}

	normalizedDefinitions := make([]ApplicationDefinition, 0, len(definitions))
	seenNames := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		normalized, normalizeErr := normalizeApplicationDefinition(definition)
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		if _, exists := seenNames[normalized.Name]; exists {
			return nil, fmt.Errorf("%w: %q", ErrDuplicateApplication, normalized.Name)
		}
		seenNames[normalized.Name] = struct{}{}
		normalizedDefinitions = append(normalizedDefinitions, normalized)
	}
	sort.Slice(normalizedDefinitions, func(left, right int) bool {
		return normalizedDefinitions[left].Name < normalizedDefinitions[right].Name
	})

	manager := &ApplicationManager{
		basePath:         rootPath,
		definitions:      append([]ApplicationDefinition(nil), normalizedDefinitions...),
		applicationNames: make([]string, 0, len(normalizedDefinitions)),
		applications:     make(map[string]*App, len(normalizedDefinitions)),
	}
	for _, definition := range normalizedDefinitions {
		manager.applicationNames = append(manager.applicationNames, definition.Name)
		applicationPath := filepath.Join(rootPath, filepath.FromSlash(definition.Path))
		runtimePath := filepath.Join(rootPath, "runtime", definition.Name)
		app := newAppWithOptions(skipDatabase, appConstructionOptions{
			applicationName:    definition.Name,
			applicationPath:    applicationPath,
			runtimePath:        runtimePath,
			copyGlobalRegistry: false,
			initialize:         false,
		}, rootPath)
		app.applicationManager = manager
		if registerErr := safeApplicationRegister(definition.Register, app); registerErr != nil {
			app.recordStartupError(fmt.Errorf("应用 %q 注册组件失败: %w", definition.Name, registerErr))
		}
		initializationErr := app.Initialize()
		manager.applications[definition.Name] = app
		if initializationErr != nil && options.failOnInitialization {
			closeErr := manager.closeApplications()
			return nil, errors.Join(fmt.Errorf("应用 %q 初始化失败: %w", definition.Name, initializationErr), closeErr)
		}
	}

	manager.defaultAppName = manager.applicationNames[0]
	if _, exists := manager.applications[defaultApplicationName]; exists {
		manager.defaultAppName = defaultApplicationName
	}
	configuredDefault := ""
	configuredDefaultSource := ""
	for _, name := range manager.applicationNames {
		if app := manager.applications[name]; app != nil && app.config != nil {
			candidate := applicationConfigString(app, "default_app")
			if candidate == "" {
				continue
			}
			if configuredDefault == "" {
				configuredDefault = candidate
				configuredDefaultSource = name
				continue
			}
			if candidate != configuredDefault {
				primaryErr := fmt.Errorf("%w: 应用 %q 声明 %q，应用 %q 声明 %q", ErrConflictingDefaultApplication, configuredDefaultSource, configuredDefault, name, candidate)
				closeErr := manager.closeApplications()
				return nil, errors.Join(primaryErr, closeErr)
			}
		}
	}
	if configuredDefault != "" {
		if _, exists := manager.applications[configuredDefault]; !exists {
			primaryErr := fmt.Errorf("默认应用 %q 未定义", configuredDefault)
			return nil, errors.Join(primaryErr, manager.closeApplications())
		}
		manager.defaultAppName = configuredDefault
	}
	resolver, resolverErr := NewApplicationResolver(manager)
	if resolverErr != nil {
		closeErr := manager.closeApplications()
		return nil, errors.Join(resolverErr, closeErr)
	}
	manager.applicationResolver = resolver
	return manager, nil
}

func normalizeManagerBasePath(basePath string) (string, error) {
	basePath = strings.TrimSpace(basePath)
	if basePath == "" {
		var err error
		basePath, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("读取项目根目录失败: %w", err)
		}
	}
	absolutePath, err := filepath.Abs(basePath)
	if err != nil {
		return "", fmt.Errorf("解析项目根目录失败: %w", err)
	}
	info, err := os.Stat(absolutePath)
	if err != nil {
		return "", fmt.Errorf("访问项目根目录失败: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("项目根目录不是目录: %s", absolutePath)
	}
	return filepath.Clean(absolutePath), nil
}

// Applications 返回应用实例映射的浅拷贝，调用方不能改变管理器的应用索引。
func (manager *ApplicationManager) Applications() map[string]*App {
	if manager == nil {
		return nil
	}
	manager.lock.Lock()
	defer manager.lock.Unlock()
	applications := make(map[string]*App, len(manager.applications))
	for name, app := range manager.applications {
		applications[name] = app
	}
	return applications
}

// Application 返回指定名称的应用实例。
func (manager *ApplicationManager) Application(name string) (*App, bool) {
	if manager == nil {
		return nil, false
	}
	manager.lock.Lock()
	defer manager.lock.Unlock()
	app, exists := manager.applications[name]
	return app, exists
}

// DefaultApplication 返回配置或默认规则选中的应用。
func (manager *ApplicationManager) DefaultApplication() *App {
	if manager == nil {
		return nil
	}
	manager.lock.Lock()
	defer manager.lock.Unlock()
	return manager.applications[manager.defaultAppName]
}

func (manager *ApplicationManager) applicationURLResolver() (*ApplicationResolver, error) {
	if manager == nil {
		return nil, ErrNilApplication
	}
	manager.applicationResolverMu.Lock()
	defer manager.applicationResolverMu.Unlock()
	if manager.applicationResolver != nil {
		return manager.applicationResolver, nil
	}
	resolver, err := NewApplicationResolver(manager)
	if err != nil {
		return nil, err
	}
	manager.applicationResolver = resolver
	return resolver, nil
}

// Boot 初始化所有应用的 Provider；任何应用失败都会在监听前回滚全部应用。
func (manager *ApplicationManager) Boot() error {
	return manager.boot(false)
}

func (manager *ApplicationManager) boot(fromRun bool) error {
	if manager == nil {
		return ErrNilApplication
	}
	manager.lock.Lock()
	if manager.closed {
		err := manager.closeErr
		if err == nil {
			err = ErrApplicationManagerClosed
		}
		manager.lock.Unlock()
		return err
	}
	if manager.running || manager.booting || manager.runPending && !fromRun {
		manager.lock.Unlock()
		return ErrApplicationManagerRunning
	}
	if manager.booted {
		err := manager.bootErr
		manager.lock.Unlock()
		return err
	}
	manager.booting = true
	applications := manager.applicationsInOrderLocked()
	manager.lock.Unlock()

	var bootErr error
	for _, application := range applications {
		if startupErr := application.StartupError(); startupErr != nil {
			bootErr = fmt.Errorf("应用 %q 初始化失败: %w", application.ApplicationName, startupErr)
			break
		}
	}
	if bootErr == nil {
		for _, application := range applications {
			if err := application.BootProviders(); err != nil {
				bootErr = fmt.Errorf("应用 %q 启动服务提供者失败: %w", application.ApplicationName, err)
				break
			}
		}
	}
	if bootErr != nil {
		closeErr := manager.closeApplicationsInOrder(applications)
		bootErr = errors.Join(bootErr, closeErr)
	}

	manager.lock.Lock()
	manager.booting = false
	manager.booted = true
	manager.bootErr = bootErr
	if bootErr != nil {
		manager.closed = true
		manager.closeErr = bootErr
	}
	manager.lock.Unlock()
	return bootErr
}

// Run 启动所有应用后只运行一次宿主内核，并在宿主退出时逆序关闭应用。
func (manager *ApplicationManager) Run(kernel Kernel) (runErr error) {
	if manager == nil {
		return ErrNilApplication
	}
	if kernel == nil {
		return ErrKernelUnavailable
	}
	manager.lock.Lock()
	if manager.closed {
		err := manager.closeErr
		if err == nil {
			err = ErrApplicationManagerClosed
		}
		manager.lock.Unlock()
		return err
	}
	if manager.booting || manager.runPending || manager.running {
		manager.lock.Unlock()
		return ErrApplicationManagerRunning
	}
	manager.runPending = true
	manager.lock.Unlock()

	if err := manager.boot(true); err != nil {
		manager.lock.Lock()
		manager.runPending = false
		manager.lock.Unlock()
		return err
	}
	manager.lock.Lock()
	applications := manager.applicationsInOrderLocked()
	manager.lock.Unlock()
	for _, application := range applications {
		application.markRunningByApplicationManager()
	}
	manager.lock.Lock()
	manager.runPending = false
	manager.running = true
	manager.lock.Unlock()

	defer func() {
		runErr = errors.Join(runErr, manager.close(true))
	}()
	if err := safeKernelRun(kernel); err != nil {
		return fmt.Errorf("宿主内核运行失败: %w", err)
	}
	return nil
}

// Close 逆序关闭全部应用，重复调用返回首次关闭结果。
func (manager *ApplicationManager) Close() error {
	return manager.close(false)
}

func (manager *ApplicationManager) close(fromRun bool) error {
	if manager == nil {
		return ErrNilApplication
	}
	manager.lock.Lock()
	if fromRun {
		manager.running = false
	}
	if manager.booting || manager.runPending || manager.running {
		manager.lock.Unlock()
		return ErrApplicationManagerRunning
	}
	if manager.closing {
		closeDone := manager.closeDone
		manager.lock.Unlock()
		<-closeDone
		manager.lock.Lock()
		err := manager.closeErr
		manager.lock.Unlock()
		return err
	}
	if manager.closed {
		err := manager.closeErr
		manager.lock.Unlock()
		return err
	}
	manager.closed = true
	manager.closing = true
	manager.closeDone = make(chan struct{})
	applications := manager.applicationsInOrderLocked()
	manager.lock.Unlock()

	closeErr := manager.closeApplicationsInOrder(applications)
	manager.lock.Lock()
	manager.closeErr = closeErr
	manager.closing = false
	close(manager.closeDone)
	manager.lock.Unlock()
	return closeErr
}

func (manager *ApplicationManager) applicationsInOrderLocked() []*App {
	applications := make([]*App, 0, len(manager.applicationNames))
	for _, name := range manager.applicationNames {
		if app := manager.applications[name]; app != nil {
			applications = append(applications, app)
		}
	}
	return applications
}

func (manager *ApplicationManager) closeApplications() error {
	manager.lock.Lock()
	applications := manager.applicationsInOrderLocked()
	manager.closed = true
	manager.lock.Unlock()
	return manager.closeApplicationsInOrder(applications)
}

func (manager *ApplicationManager) closeApplicationsInOrder(applications []*App) error {
	var closeErr error
	for index := len(applications) - 1; index >= 0; index-- {
		application := applications[index]
		if err := application.closeFromApplicationManager(); err != nil {
			closeErr = errors.Join(closeErr, fmt.Errorf("关闭应用 %q 失败: %w", application.ApplicationName, err))
		}
	}
	return closeErr
}
