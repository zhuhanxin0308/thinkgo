package framework

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"thinkgo/framework/cache"
	cacheDriver "thinkgo/framework/cache/driver"
)

const maxConfiguredCacheStores = 64

var appCacheStoreNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

type appCacheStoreSpec struct {
	name        string
	driverType  string
	path        string
	redisConfig map[string]interface{}
}

// createAppCache 先完整校验全部 store，再创建驱动并注册默认别名，避免错误配置静默回退。
func createAppCache(app *App, rawConfig map[string]interface{}) (*cache.Cache, error) {
	if app == nil {
		return nil, ErrNilApplication
	}
	defaultStore, specs, err := parseAppCacheConfig(app.BasePath, rawConfig)
	if err != nil {
		return nil, err
	}

	// Redis 构造不发起网络请求，优先创建可让其严格配置错误在文件目录创建前返回。
	sort.SliceStable(specs, func(left, right int) bool {
		leftRank := cacheDriverCreationRank(specs[left].driverType)
		rightRank := cacheDriverCreationRank(specs[right].driverType)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		return specs[left].name < specs[right].name
	})
	drivers := make(map[string]cache.Driver, len(specs))
	for _, spec := range specs {
		driver, createErr := createAppCacheDriver(spec)
		if createErr != nil {
			return nil, errors.Join(
				fmt.Errorf("创建缓存 store %q 失败: %w", spec.name, createErr),
				closeAppCacheDrivers(drivers),
			)
		}
		drivers[spec.name] = driver
	}

	manager := cache.NewCache(app.Debug, drivers[defaultStore])
	names := make([]string, 0, len(drivers))
	for name := range drivers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err = manager.RegisterStore(name, drivers[name]); err != nil {
			return nil, errors.Join(
				fmt.Errorf("注册缓存 store %q 失败: %w", name, err),
				manager.Close(),
			)
		}
	}
	return manager, nil
}

func parseAppCacheConfig(basePath string, rawConfig map[string]interface{}) (string, []appCacheStoreSpec, error) {
	if strings.TrimSpace(basePath) == "" {
		return "", nil, errors.New("缓存配置缺少应用根目录")
	}
	for key := range rawConfig {
		if key != "default" && key != "stores" {
			return "", nil, fmt.Errorf("缓存配置包含未知字段 %q", key)
		}
	}
	rawDefault, exists := rawConfig["default"]
	defaultStore, valid := rawDefault.(string)
	if !exists || !valid || !validAppCacheStoreName(defaultStore) {
		return "", nil, errors.New("缓存配置 default 必须是合法的非空 store 名称")
	}
	rawStores, valid := rawConfig["stores"].(map[string]interface{})
	if !valid || len(rawStores) == 0 || len(rawStores) > maxConfiguredCacheStores {
		return "", nil, fmt.Errorf("缓存配置 stores 必须包含 1 至 %d 个对象", maxConfiguredCacheStores)
	}
	if _, exists = rawStores[defaultStore]; !exists {
		return "", nil, fmt.Errorf("默认缓存 store %q 不存在", defaultStore)
	}

	names := make([]string, 0, len(rawStores))
	for name := range rawStores {
		names = append(names, name)
	}
	sort.Strings(names)
	specs := make([]appCacheStoreSpec, 0, len(names))
	for _, name := range names {
		if !validAppCacheStoreName(name) {
			return "", nil, fmt.Errorf("缓存 store 名称 %q 非法", name)
		}
		storeConfig, ok := rawStores[name].(map[string]interface{})
		if !ok {
			return "", nil, fmt.Errorf("缓存 store %q 的配置必须是对象", name)
		}
		spec, parseErr := parseAppCacheStore(basePath, name, storeConfig)
		if parseErr != nil {
			return "", nil, parseErr
		}
		specs = append(specs, spec)
	}
	return defaultStore, specs, nil
}

func parseAppCacheStore(basePath, name string, storeConfig map[string]interface{}) (appCacheStoreSpec, error) {
	rawType, exists := storeConfig["type"]
	driverType, valid := rawType.(string)
	if !exists || !valid || driverType == "" || strings.TrimSpace(driverType) != driverType {
		return appCacheStoreSpec{}, fmt.Errorf("缓存 store %q 的 type 必须是非空字符串", name)
	}
	spec := appCacheStoreSpec{name: name, driverType: driverType}
	switch driverType {
	case "memory":
		if err := rejectUnknownCacheStoreFields(name, storeConfig, "type"); err != nil {
			return appCacheStoreSpec{}, err
		}
	case "file":
		if err := rejectUnknownCacheStoreFields(name, storeConfig, "type", "path"); err != nil {
			return appCacheStoreSpec{}, err
		}
		rawPath, pathExists := storeConfig["path"]
		pathValue, pathValid := rawPath.(string)
		if !pathExists || !pathValid {
			return appCacheStoreSpec{}, fmt.Errorf("文件缓存 store %q 的 path 必须是非空字符串", name)
		}
		path, err := normalizeAppStoragePath(basePath, pathValue)
		if err != nil {
			return appCacheStoreSpec{}, fmt.Errorf("文件缓存 store %q 路径非法: %w", name, err)
		}
		spec.path = path
	case "redis":
		if err := rejectUnknownCacheStoreFields(
			name,
			storeConfig,
			"type", "host", "port", "password", "select", "timeout_ms", "prefix", "allow_flush_db",
		); err != nil {
			return appCacheStoreSpec{}, err
		}
		spec.redisConfig = make(map[string]interface{}, len(storeConfig)-1)
		for key, value := range storeConfig {
			if key != "type" {
				spec.redisConfig[key] = value
			}
		}
	default:
		return appCacheStoreSpec{}, fmt.Errorf("缓存 store %q 使用不支持的驱动 %q", name, driverType)
	}
	return spec, nil
}

func rejectUnknownCacheStoreFields(name string, config map[string]interface{}, allowed ...string) error {
	allowedSet := make(map[string]bool, len(allowed))
	for _, field := range allowed {
		allowedSet[field] = true
	}
	for field := range config {
		if !allowedSet[field] {
			return fmt.Errorf("缓存 store %q 包含未知配置项 %q", name, field)
		}
	}
	return nil
}

func validAppCacheStoreName(name string) bool {
	return name == strings.TrimSpace(name) && appCacheStoreNamePattern.MatchString(name)
}

func cacheDriverCreationRank(driverType string) int {
	if driverType == "file" {
		return 1
	}
	return 0
}

func createAppCacheDriver(spec appCacheStoreSpec) (cache.Driver, error) {
	switch spec.driverType {
	case "memory":
		return cacheDriver.NewMemory(), nil
	case "file":
		return cacheDriver.NewFile(spec.path)
	case "redis":
		return cacheDriver.NewRedis(spec.redisConfig)
	default:
		return nil, fmt.Errorf("不支持的缓存驱动 %q", spec.driverType)
	}
}

func closeAppCacheDrivers(drivers map[string]cache.Driver) error {
	var resultErr error
	for name, driver := range drivers {
		if closer, ok := driver.(interface{ Close() error }); ok {
			if err := closer.Close(); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("关闭缓存 store %q 失败: %w", name, err))
			}
		}
	}
	return resultErr
}
