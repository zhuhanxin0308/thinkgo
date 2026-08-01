package framework

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"thinkgo/framework/cache"
	cacheDriver "thinkgo/framework/cache/driver"
)

const maxConfiguredCacheStores = 64

const fileCacheResourcePrefix = "file:"

var appCacheStoreNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

type appCacheStoreSpec struct {
	name             string
	driverType       string
	path             string
	confineToRuntime bool
	maxMemoryEntries int
	redisConfig      map[string]interface{}
}

// createAppCache 先完整校验全部 store，再创建驱动并注册默认别名，避免错误配置静默回退。
func createAppCache(app *App, rawConfig map[string]interface{}) (*cache.Cache, error) {
	if app == nil {
		return nil, ErrNilApplication
	}
	defaultStore, specs, err := parseAppCacheConfigForApp(app, rawConfig)
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
		if spec.driverType == "file" && spec.confineToRuntime {
			if pathErr := validateAppCachePathWithinRuntime(app.ApplicationRuntimePath(), spec.path); pathErr != nil {
				return nil, errors.Join(
					fmt.Errorf("创建缓存 store %q 失败: %w", spec.name, pathErr),
					closeAppCacheDrivers(drivers),
				)
			}
		}
		driver, createErr := createAppCacheDriver(spec)
		if createErr != nil {
			return nil, errors.Join(
				fmt.Errorf("创建缓存 store %q 失败: %w", spec.name, createErr),
				closeAppCacheDrivers(drivers),
			)
		}
		if spec.driverType == "file" && spec.confineToRuntime {
			if pathErr := validateAppCacheDriverPathWithinRuntime(app.ApplicationRuntimePath(), driver); pathErr != nil {
				return nil, errors.Join(
					fmt.Errorf("创建缓存 store %q 失败: %w", spec.name, pathErr),
					closeAppCacheDrivers(drivers),
					closeCacheDriver(driver),
				)
			}
		}
		drivers[spec.name] = driver
	}

	manager := cache.NewCache(nil, drivers[defaultStore])
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

// validateAppCacheDriverPathWithinRuntime 在驱动创建后再次校验真实目录，阻断校验和创建之间的符号链接竞态。
func validateAppCacheDriverPathWithinRuntime(runtimePath string, driver cache.Driver) error {
	identified, ok := driver.(interface{ CacheResourceIdentity() string })
	if !ok {
		return errors.New("文件缓存驱动缺少资源标识")
	}
	identity := identified.CacheResourceIdentity()
	if !strings.HasPrefix(identity, fileCacheResourcePrefix) {
		return errors.New("文件缓存驱动资源标识无效")
	}
	cachePath := strings.TrimPrefix(identity, fileCacheResourcePrefix)
	if cachePath == "" {
		return errors.New("文件缓存驱动路径为空")
	}
	if err := validateAppCachePathWithinRuntime(runtimePath, cachePath); err != nil {
		return fmt.Errorf("文件缓存真实目录越出应用 runtime: %w", err)
	}
	return nil
}

func parseAppCacheConfigForApp(app *App, rawConfig map[string]interface{}) (string, []appCacheStoreSpec, error) {
	if app == nil {
		return "", nil, ErrNilApplication
	}
	return parseAppCacheConfigWithResolver(app.BasePath, rawConfig, app.resolveStoragePath)
}

func parseAppCacheConfigWithResolver(basePath string, rawConfig map[string]interface{}, resolvePath func(string) (string, error)) (string, []appCacheStoreSpec, error) {
	if strings.TrimSpace(basePath) == "" {
		return "", nil, errors.New("缓存配置缺少应用根目录")
	}
	if resolvePath == nil {
		return "", nil, errors.New("缓存配置缺少路径解析器")
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
		spec, parseErr := parseAppCacheStore(resolvePath, name, storeConfig)
		if parseErr != nil {
			return "", nil, parseErr
		}
		specs = append(specs, spec)
	}
	if err := validateAppCacheStoreIsolation(specs); err != nil {
		return "", nil, err
	}
	return defaultStore, specs, nil
}

func parseAppCacheStore(resolvePath func(string) (string, error), name string, storeConfig map[string]interface{}) (appCacheStoreSpec, error) {
	rawType, exists := storeConfig["type"]
	driverType, valid := rawType.(string)
	if !exists || !valid || driverType == "" || strings.TrimSpace(driverType) != driverType {
		return appCacheStoreSpec{}, fmt.Errorf("缓存 store %q 的 type 必须是非空字符串", name)
	}
	spec := appCacheStoreSpec{name: name, driverType: driverType}
	switch driverType {
	case "memory":
		if err := rejectUnknownCacheStoreFields(name, storeConfig, "type", "max_entries"); err != nil {
			return appCacheStoreSpec{}, err
		}
		if rawMaxEntries, exists := storeConfig["max_entries"]; exists {
			maxEntries, err := readConfigIntValue(rawMaxEntries)
			if err != nil {
				return appCacheStoreSpec{}, fmt.Errorf("内存缓存 store %q 的 max_entries 必须是非负整数: %w", name, err)
			}
			spec.maxMemoryEntries = maxEntries
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
		path, err := resolvePath(pathValue)
		if err != nil {
			return appCacheStoreSpec{}, fmt.Errorf("文件缓存 store %q 路径非法: %w", name, err)
		}
		spec.path = path
		spec.confineToRuntime = !filepath.IsAbs(pathValue) && filepath.VolumeName(pathValue) == ""
	case "redis":
		if err := rejectUnknownCacheStoreFields(
			name,
			storeConfig,
			"type", "host", "port", "password", "select", "timeout_ms", "prefix", "allow_flush_db",
			"tls_enable", "tls_server_name", "tls_insecure_skip_verify",
		); err != nil {
			return appCacheStoreSpec{}, err
		}
		if err := validateAppRedisPrefix(name, storeConfig); err != nil {
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

// validateAppCachePathWithinRuntime 在创建目录前解析已有路径组件，阻止相对配置通过符号链接逃出应用 runtime。
func validateAppCachePathWithinRuntime(runtimePath, cachePath string) error {
	runtimeReal, err := resolveExistingStoragePath(runtimePath)
	if err != nil {
		return fmt.Errorf("解析应用 runtime 失败: %w", err)
	}
	cacheReal, err := resolveExistingStoragePath(cachePath)
	if err != nil {
		return fmt.Errorf("解析缓存目录失败: %w", err)
	}
	if runtime.GOOS == "windows" {
		runtimeReal = strings.ToLower(runtimeReal)
		cacheReal = strings.ToLower(cacheReal)
	}
	relative, err := filepath.Rel(runtimeReal, cacheReal)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("缓存目录必须位于应用 runtime 内: %q", cachePath)
	}
	return nil
}

// resolveExistingStoragePath 解析路径中最后一个已存在组件的真实位置，并保留尚不存在的尾部。
func resolveExistingStoragePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	current := filepath.Clean(abs)
	missing := make([]string, 0)
	for {
		_, statErr := os.Lstat(current)
		if statErr == nil {
			realPath, evalErr := filepath.EvalSymlinks(current)
			if evalErr != nil {
				return "", evalErr
			}
			for index := len(missing) - 1; index >= 0; index-- {
				realPath = filepath.Join(realPath, missing[index])
			}
			return filepath.Clean(realPath), nil
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return "", statErr
		}
		parent := filepath.Dir(current)
		if parent == current {
			return filepath.Clean(abs), nil
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

// validateAppRedisPrefix 要求应用配置默认使用非空 namespace；全库清理只能显式授权。
func validateAppRedisPrefix(name string, storeConfig map[string]interface{}) error {
	rawPrefix, hasPrefix := storeConfig["prefix"]
	rawAllowFlush, hasAllowFlush := storeConfig["allow_flush_db"]
	allowFlush, allowFlushValid := rawAllowFlush.(bool)
	if hasAllowFlush && !allowFlushValid {
		return fmt.Errorf("redis 缓存 store %q 的 allow_flush_db 必须是布尔值", name)
	}
	if !hasPrefix {
		if !allowFlush {
			return fmt.Errorf("redis 缓存 store %q 必须配置非空 prefix；独占数据库才可显式设置 allow_flush_db=true", name)
		}
		return nil
	}
	prefix, valid := rawPrefix.(string)
	if !valid {
		return fmt.Errorf("redis 缓存 store %q 的 prefix 必须是非空字符串", name)
	}
	if strings.TrimSpace(prefix) == "" && !allowFlush {
		return fmt.Errorf("redis 缓存 store %q 的 prefix 必须是非空字符串", name)
	}
	return nil
}

// validateAppCacheStoreIsolation 拒绝应用配置中会共享文件目录或 Redis namespace 的多个 store。
func validateAppCacheStoreIsolation(specs []appCacheStoreSpec) error {
	seen := make(map[string]string, len(specs))
	for _, spec := range specs {
		identity, valid := appCacheResourceIdentity(spec)
		if !valid {
			continue
		}
		if previous, exists := seen[identity]; exists {
			return fmt.Errorf("缓存 store %q 与 %q 共享同一后端 namespace，必须使用不同 path 或 prefix", previous, spec.name)
		}
		seen[identity] = spec.name
	}
	return nil
}

func appCacheResourceIdentity(spec appCacheStoreSpec) (string, bool) {
	switch spec.driverType {
	case "file":
		path := filepath.Clean(spec.path)
		if runtime.GOOS == "windows" {
			path = strings.ToLower(path)
		}
		return fileCacheResourcePrefix + path, path != "."
	case "redis":
		return redisCacheResourceIdentity(spec.redisConfig)
	default:
		return "", false
	}
}

func redisCacheResourceIdentity(config map[string]interface{}) (string, bool) {
	host := "127.0.0.1"
	if raw, exists := config["host"]; exists {
		value, valid := raw.(string)
		if !valid {
			return "", false
		}
		host = strings.ToLower(value)
	}
	port := 6379
	if raw, exists := config["port"]; exists {
		value, err := readConfigIntValue(raw)
		if err != nil {
			return "", false
		}
		port = value
	}
	selectedDB := 0
	if raw, exists := config["select"]; exists {
		value, err := readConfigIntValue(raw)
		if err != nil {
			return "", false
		}
		selectedDB = value
	}
	prefix := ""
	if raw, exists := config["prefix"]; exists {
		value, valid := raw.(string)
		if !valid {
			return "", false
		}
		prefix = value
	}
	return fmt.Sprintf("redis:%s:%d:%d:%s", host, port, selectedDB, prefix), true
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
		return cacheDriver.NewMemoryWithMaxEntries(spec.maxMemoryEntries)
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

func closeCacheDriver(driver cache.Driver) error {
	if closer, ok := driver.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}
