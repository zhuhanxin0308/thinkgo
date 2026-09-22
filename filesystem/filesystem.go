package filesystem

import (
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"strings"
	"sync"
)

var (
	// ErrDiskNotFound 表示配置中不存在指定磁盘。
	ErrDiskNotFound = errors.New("文件系统磁盘不存在")
	// ErrDriverNotSupported 表示磁盘声明的驱动类型尚未注册。
	ErrDriverNotSupported = errors.New("文件系统驱动不受支持")
	// ErrFilesystemClosed 表示文件系统管理器或磁盘已经关闭。
	ErrFilesystemClosed = errors.New("文件系统已经关闭")
	// ErrInvalidConfiguration 表示磁盘配置不能转换为有效驱动参数。
	ErrInvalidConfiguration = errors.New("文件系统配置无效")
)

// Driver 定义 ThinkPHP Filesystem 对业务代码公开的磁盘能力。
//
// PHP 异常在 Go 中以 error 返回；可选的 Flysystem 配置数组使用可选 map 表达。
type Driver interface {
	FileExists(path string) (bool, error)
	DirectoryExists(path string) (bool, error)
	Has(path string) (bool, error)
	Write(path string, contents string, options ...map[string]interface{}) error
	WriteStream(path string, contents io.Reader, options ...map[string]interface{}) error
	Read(path string) (string, error)
	ReadStream(path string) (io.ReadCloser, error)
	Delete(path string) error
	DeleteDirectory(path string) error
	CreateDirectory(path string, options ...map[string]interface{}) error
	ListContents(path string, deep bool) ([]Entry, error)
	Move(source string, destination string, options ...map[string]interface{}) error
	Copy(source string, destination string, options ...map[string]interface{}) error
	LastModified(path string) (int64, error)
	FileSize(path string) (int64, error)
	MimeType(path string) (string, error)
	SetVisibility(path string, visibility string) error
	Visibility(path string) (string, error)
	Checksum(path string, options ...map[string]interface{}) (string, error)
	Path(path string) (string, error)
	URL(path string) (string, error)
	PutFile(path string, file *multipart.FileHeader, arguments ...interface{}) (string, error)
	PutFileAs(path string, file *multipart.FileHeader, name string, options ...map[string]interface{}) (string, error)
	Close() error
}

// DriverFactory 按单个磁盘配置创建驱动，等价于 ThinkPHP Manager 的驱动解析扩展点。
type DriverFactory func(configuration map[string]interface{}) (Driver, error)

// Filesystem 管理具名磁盘，并与 ThinkPHP Manager 一致在首次 disk 调用时创建驱动。
type Filesystem struct {
	mu         sync.Mutex
	config     map[string]interface{}
	drivers    map[string]Driver
	factories  map[string]DriverFactory
	closed     bool
	closeDone  chan struct{}
	closeError error
}

// New 创建文件系统管理器；磁盘配置在首次使用前不会触发目录或驱动初始化。
func New(configuration map[string]interface{}) *Filesystem {
	manager := &Filesystem{
		config:    cloneMap(configuration),
		drivers:   make(map[string]Driver),
		factories: make(map[string]DriverFactory),
		closeDone: make(chan struct{}),
	}
	manager.factories["local"] = func(diskConfiguration map[string]interface{}) (Driver, error) {
		configuration, err := parseLocalConfig(diskConfiguration)
		if err != nil {
			return nil, err
		}
		return NewLocal(configuration)
	}
	return manager
}

// Disk 返回指定磁盘；省略名称或传入空名称时使用 default 配置。
func (manager *Filesystem) Disk(name ...string) (Driver, error) {
	if manager == nil {
		return nil, ErrFilesystemClosed
	}
	if len(name) > 1 {
		return nil, fmt.Errorf("%w: disk 最多接收一个名称", ErrInvalidConfiguration)
	}
	diskName := ""
	if len(name) == 1 {
		diskName = name[0]
	}
	if diskName == "" {
		diskName = manager.GetDefaultDriver()
	}
	if diskName == "" {
		return nil, fmt.Errorf("%w: 未配置默认磁盘", ErrDiskNotFound)
	}

	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return nil, ErrFilesystemClosed
	}
	if driver, exists := manager.drivers[diskName]; exists {
		manager.mu.Unlock()
		return driver, nil
	}
	configuration, exists := manager.diskConfigurationLocked(diskName)
	if !exists {
		manager.mu.Unlock()
		return nil, fmt.Errorf("%w: %s", ErrDiskNotFound, diskName)
	}
	driverType := "local"
	if configuredType, configured := configuration["type"]; configured {
		value, ok := configuredType.(string)
		if !ok || strings.TrimSpace(value) == "" {
			manager.mu.Unlock()
			return nil, fmt.Errorf("%w: 磁盘 %s 的 type 必须是非空字符串", ErrInvalidConfiguration, diskName)
		}
		driverType = strings.ToLower(strings.TrimSpace(value))
	}
	factory, supported := manager.factories[driverType]
	manager.mu.Unlock()
	if !supported {
		return nil, fmt.Errorf("%w: %s", ErrDriverNotSupported, driverType)
	}

	driver, err := factory(cloneMap(configuration))
	if err != nil {
		return nil, fmt.Errorf("创建磁盘 %s 失败: %w", diskName, err)
	}
	if driver == nil {
		return nil, fmt.Errorf("%w: 驱动工厂 %s 返回空实例", ErrInvalidConfiguration, driverType)
	}

	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed {
		_ = driver.Close()
		return nil, ErrFilesystemClosed
	}
	if existing, exists := manager.drivers[diskName]; exists {
		_ = driver.Close()
		return existing, nil
	}
	manager.drivers[diskName] = driver
	return driver, nil
}

// GetConfig 获取 filesystem 配置；参数依次对应 ThinkPHP 的 name 和 default。
func (manager *Filesystem) GetConfig(arguments ...interface{}) interface{} {
	if manager == nil {
		return nil
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if len(arguments) == 0 || arguments[0] == nil {
		return cloneMap(manager.config)
	}
	name, ok := arguments[0].(string)
	if !ok {
		return configurationDefault(arguments)
	}
	value, exists := lookupConfiguration(manager.config, name)
	if !exists {
		return configurationDefault(arguments)
	}
	return cloneValue(value)
}

// GetDiskConfig 获取具名磁盘配置；可选参数依次对应配置名和默认值。
func (manager *Filesystem) GetDiskConfig(disk string, arguments ...interface{}) (interface{}, error) {
	if manager == nil {
		return nil, ErrFilesystemClosed
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	configuration, exists := manager.diskConfigurationLocked(disk)
	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrDiskNotFound, disk)
	}
	if len(arguments) == 0 || arguments[0] == nil {
		return cloneMap(configuration), nil
	}
	name, ok := arguments[0].(string)
	if !ok {
		return configurationDefault(arguments), nil
	}
	value, found := lookupConfiguration(configuration, name)
	if !found {
		return configurationDefault(arguments), nil
	}
	return cloneValue(value), nil
}

// GetDefaultDriver 返回 default 配置中的磁盘名称。
func (manager *Filesystem) GetDefaultDriver() string {
	if manager == nil {
		return ""
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	value, _ := manager.config["default"].(string)
	return value
}

// Extend 注册自定义驱动工厂，作为 Go 中自定义 ThinkPHP 文件系统驱动的扩展点。
func (manager *Filesystem) Extend(driverType string, factory DriverFactory) error {
	if manager == nil {
		return ErrFilesystemClosed
	}
	driverType = strings.ToLower(strings.TrimSpace(driverType))
	if driverType == "" || factory == nil {
		return fmt.Errorf("%w: 驱动类型和工厂不能为空", ErrInvalidConfiguration)
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed {
		return ErrFilesystemClosed
	}
	manager.factories[driverType] = factory
	return nil
}

// ForgetDriver 移除已经创建的磁盘实例，后续 disk 调用会按当前配置重新创建。
func (manager *Filesystem) ForgetDriver(names ...string) *Filesystem {
	if manager == nil {
		return nil
	}
	if len(names) == 0 {
		names = []string{manager.GetDefaultDriver()}
	}
	manager.mu.Lock()
	forgotten := make([]Driver, 0, len(names))
	for _, name := range names {
		if driver, exists := manager.drivers[name]; exists {
			forgotten = append(forgotten, driver)
			delete(manager.drivers, name)
		}
	}
	manager.mu.Unlock()
	for _, driver := range forgotten {
		_ = driver.Close()
	}
	return manager
}

// Close 关闭所有已经实例化的磁盘；未使用的磁盘不会在关闭时被创建。
func (manager *Filesystem) Close() error {
	if manager == nil {
		return nil
	}
	manager.mu.Lock()
	if manager.closed {
		done := manager.closeDone
		if done == nil {
			done = make(chan struct{})
			close(done)
		}
		manager.mu.Unlock()
		<-done
		manager.mu.Lock()
		err := manager.closeError
		manager.mu.Unlock()
		return err
	}
	manager.closed = true
	if manager.closeDone == nil {
		manager.closeDone = make(chan struct{})
	}
	done := manager.closeDone
	drivers := make([]Driver, 0, len(manager.drivers))
	for _, driver := range manager.drivers {
		drivers = append(drivers, driver)
	}
	manager.drivers = make(map[string]Driver)
	manager.mu.Unlock()

	var closeErr error
	for _, driver := range drivers {
		closeErr = errors.Join(closeErr, driver.Close())
	}
	manager.mu.Lock()
	manager.closeError = closeErr
	close(done)
	manager.mu.Unlock()
	return closeErr
}

func (manager *Filesystem) diskConfigurationLocked(name string) (map[string]interface{}, bool) {
	disks, ok := manager.config["disks"].(map[string]interface{})
	if !ok {
		return nil, false
	}
	raw, exists := disks[name]
	if !exists {
		return nil, false
	}
	configuration, ok := raw.(map[string]interface{})
	return configuration, ok && len(configuration) > 0
}

func configurationDefault(arguments []interface{}) interface{} {
	if len(arguments) > 1 {
		return cloneValue(arguments[1])
	}
	return nil
}

func lookupConfiguration(configuration map[string]interface{}, name string) (interface{}, bool) {
	if name == "" {
		return configuration, true
	}
	var current interface{} = configuration
	for _, part := range strings.Split(name, ".") {
		values, ok := current.(map[string]interface{})
		if !ok {
			return nil, false
		}
		current, ok = values[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func cloneMap(source map[string]interface{}) map[string]interface{} {
	cloned := make(map[string]interface{}, len(source))
	for key, value := range source {
		cloned[key] = cloneValue(value)
	}
	return cloned
}

func cloneValue(value interface{}) interface{} {
	switch current := value.(type) {
	case map[string]interface{}:
		return cloneMap(current)
	case []interface{}:
		cloned := make([]interface{}, len(current))
		for index, item := range current {
			cloned[index] = cloneValue(item)
		}
		return cloned
	default:
		return current
	}
}
