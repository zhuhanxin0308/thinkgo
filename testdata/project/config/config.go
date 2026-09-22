package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"unicode"
)

const (
	// maxConfigCacheEntries 限制点路径缓存的最大条目数，避免动态查询键耗尽进程内存。
	maxConfigCacheEntries = 512
	// maxConfigFileBytes 限制单个 JSON 配置文件的最大字节数。
	maxConfigFileBytes int64 = 8 * 1024 * 1024
	// maxConfigTotalBytes 限制一次批量加载配置文件的总字节数。
	maxConfigTotalBytes int64 = 64 * 1024 * 1024
	// maxConfigFileCount 限制一次批量加载的 JSON 文件数量。
	maxConfigFileCount = 1024
	// maxConfigDirectoryDepth 限制配置目录递归深度，避免意外扫描过深目录树。
	maxConfigDirectoryDepth = 8
	// maxConfigJSONDepth 限制 JSON 容器嵌套深度，避免解析过深数据消耗过多栈空间。
	maxConfigJSONDepth = 64
)

var (
	// ErrConfigFileTooLarge 表示配置文件超过单文件大小限制。
	ErrConfigFileTooLarge = errors.New("config file is too large")
	// ErrConfigTotalTooLarge 表示批量配置总大小超过限制。
	ErrConfigTotalTooLarge = errors.New("config files are too large")
	// ErrConfigFileCountExceeded 表示批量配置文件数量超过限制。
	ErrConfigFileCountExceeded = errors.New("too many config files")
	// ErrConfigDirectoryTooDeep 表示配置目录递归深度超过限制。
	ErrConfigDirectoryTooDeep = errors.New("config directory is too deep")
	// ErrConfigSymlinkNotAllowed 表示配置加载路径中出现符号链接。
	ErrConfigSymlinkNotAllowed = errors.New("config symlink is not allowed")
	// ErrConfigFileChanged 表示文件在检查与打开之间发生了替换。
	ErrConfigFileChanged = errors.New("config file changed during open")
	// ErrConfigDuplicateKey 表示同一 JSON 对象中存在重复键。
	ErrConfigDuplicateKey = errors.New("duplicate config key")
	// ErrConfigDuplicateNamespace 表示批量加载时出现重复的配置命名空间。
	ErrConfigDuplicateNamespace = errors.New("duplicate config namespace")
	// ErrConfigJSONTooDeep 表示 JSON 嵌套深度超过限制。
	ErrConfigJSONTooDeep = errors.New("config JSON is too deep")
	// ErrConfigInvalidPath 表示点路径包含空路径段。
	ErrConfigInvalidPath = errors.New("invalid config path")
	// ErrConfigInvalidNamespace 表示配置命名空间不是单一的安全路径段。
	ErrConfigInvalidNamespace = errors.New("invalid config namespace")
	// ErrConfigPermissionsTooOpen 表示 Unix 配置文件可被组或其他用户改写。
	ErrConfigPermissionsTooOpen = errors.New("config file permissions are too open")
)

// Config 管理应用配置，并为读取方提供隔离的快照。
type Config struct {
	config      map[string]interface{}
	lookupCache map[string]configLookupCacheEntry
	lookupKeys  []string
	pathCache   map[string][]string
	pathKeys    []string
	lock        sync.RWMutex
}

// configLookupCacheEntry 缓存点路径查询结果，避免热点配置重复拆分与逐层遍历。
type configLookupCacheEntry struct {
	value interface{}
	found bool
}

// configFileCandidate 表示已经通过目录扫描限制的配置文件。
type configFileCandidate struct {
	path string
	name string
}

// NewConfig 创建一个新的配置管理器。
func NewConfig() *Config {
	return &Config{
		config:      make(map[string]interface{}),
		lookupCache: make(map[string]configLookupCacheEntry),
		pathCache:   make(map[string][]string),
	}
}

// Clone 返回与当前配置完全隔离的递归快照。
// 查询缓存属于实例内部状态，不会复制到新的工作副本。
func (c *Config) Clone() *Config {
	cloned := NewConfig()
	if c == nil {
		return cloned
	}
	c.lock.RLock()
	cloned.config = deepCopyMap(c.config)
	c.lock.RUnlock()
	return cloned
}

// ReplaceWithSnapshot 用输入配置的递归快照原子替换当前全部配置。
// 输入为 nil 时替换为空配置；调用完成后两者不共享可变集合。
func (c *Config) ReplaceWithSnapshot(snapshot *Config) {
	if c == nil {
		return
	}
	replacement := make(map[string]interface{})
	if snapshot != nil {
		replacement = snapshot.GetMap("")
	}
	c.lock.Lock()
	c.config = replacement
	c.invalidateLookupCacheLocked()
	c.lock.Unlock()
}

// Load 加载一个 JSON 配置文件到命名空间。
func (c *Config) Load(file string, name string) error {
	name, err := normalizeConfigNamespace(name)
	if err != nil {
		return err
	}
	data, err := loadConfigFile(file)
	if err != nil {
		return err
	}

	c.lock.Lock()
	defer c.lock.Unlock()
	c.ensureInitializedLocked()

	if name != "" {
		// ThinkPHP 的命名空间合并只在命名空间的第一层合并，嵌套 map 会整体替换。
		existing, ok := c.config[name].(map[string]interface{})
		if !ok {
			existing = make(map[string]interface{})
		}
		mergeConfigMap(existing, data)
		c.config[name] = existing
	} else {
		mergeConfigMap(c.config, data)
	}
	c.invalidateLookupCacheLocked()
	return nil
}

// mergeConfigMap 按 ThinkPHP 的一层规则合并 map，后加载的同名键覆盖先加载的键。
func mergeConfigMap(dst, src map[string]interface{}) {
	for key, value := range src {
		dst[key] = deepCopyValue(value)
	}
}

// loadConfigFile 读取并校验单个 JSON 配置文件，所有 I/O 都在配置锁外完成。
func loadConfigFile(file string) (map[string]interface{}, error) {
	info, err := os.Lstat(file)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: %s", ErrConfigSymlinkNotAllowed, file)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("config path is not a regular file: %s", file)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("%w: %s", ErrConfigPermissionsTooOpen, file)
	}
	if info.Size() > maxConfigFileBytes {
		return nil, fmt.Errorf("%w: %s", ErrConfigFileTooLarge, file)
	}

	// #nosec G304 -- 文件路径来自应用显式配置加载流程，且已先拒绝符号链接和非普通文件。
	opened, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer opened.Close()
	openedInfo, err := opened.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, openedInfo) {
		return nil, fmt.Errorf("%w: %s", ErrConfigFileChanged, file)
	}

	content, err := io.ReadAll(io.LimitReader(opened, maxConfigFileBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > maxConfigFileBytes {
		return nil, fmt.Errorf("%w: %s", ErrConfigFileTooLarge, file)
	}
	return decodeConfigJSON(content)
}

// decodeConfigJSON 在标准反序列化前检查重复键、尾随数据和嵌套深度。
func decodeConfigJSON(content []byte) (map[string]interface{}, error) {
	decoder := json.NewDecoder(bytes.NewReader(content))
	if err := validateJSONValue(decoder, 0, "$"); err != nil {
		return nil, err
	}

	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errors.New("config JSON contains trailing data")
		}
		return nil, err
	}

	decoder = json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	var decoded map[string]interface{}
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errors.New("config JSON contains trailing data")
		}
		return nil, err
	}
	if decoded == nil {
		return nil, errors.New("config JSON root must be an object")
	}
	return normalizeConfigMap(decoded, 0, "$")
}

// validateJSONValue 使用 Token 遍历 JSON，确保对象键在大小写不敏感规则下唯一。
func validateJSONValue(decoder *json.Decoder, depth int, path string) error {
	if depth > maxConfigJSONDepth {
		return fmt.Errorf("%w at %s", ErrConfigJSONTooDeep, path)
	}

	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch delimiter := token.(type) {
	case json.Delim:
		switch delimiter {
		case '{':
			keys := make(map[string]struct{})
			for decoder.More() {
				keyToken, tokenErr := decoder.Token()
				if tokenErr != nil {
					return tokenErr
				}
				key, ok := keyToken.(string)
				if !ok {
					return fmt.Errorf("config object key at %s is not a string", path)
				}
				canonicalKey := strings.ToLower(key)
				if _, exists := keys[canonicalKey]; exists {
					return fmt.Errorf("%w at %s.%s", ErrConfigDuplicateKey, path, key)
				}
				keys[canonicalKey] = struct{}{}
				if err := validateJSONValue(decoder, depth+1, path+"."+key); err != nil {
					return err
				}
			}
			end, tokenErr := decoder.Token()
			if tokenErr != nil {
				return tokenErr
			}
			if end != json.Delim('}') {
				return fmt.Errorf("config object at %s is not closed", path)
			}
		case '[':
			index := 0
			for decoder.More() {
				if err := validateJSONValue(decoder, depth+1, fmt.Sprintf("%s[%d]", path, index)); err != nil {
					return err
				}
				index++
			}
			end, tokenErr := decoder.Token()
			if tokenErr != nil {
				return tokenErr
			}
			if end != json.Delim(']') {
				return fmt.Errorf("config array at %s is not closed", path)
			}
		default:
			return fmt.Errorf("unexpected JSON delimiter %q at %s", delimiter, path)
		}
	}
	return nil
}

// normalizeConfigMap 将 JSON 对象键统一为小写，并递归复制，保证点路径读取规则一致。
func normalizeConfigMap(src map[string]interface{}, depth int, path string) (map[string]interface{}, error) {
	if depth > maxConfigJSONDepth {
		return nil, fmt.Errorf("%w at %s", ErrConfigJSONTooDeep, path)
	}

	dst := make(map[string]interface{}, len(src))
	for key, value := range src {
		canonicalKey := strings.ToLower(key)
		if _, exists := dst[canonicalKey]; exists {
			return nil, fmt.Errorf("%w at %s.%s", ErrConfigDuplicateKey, path, key)
		}
		normalized, err := normalizeConfigValue(value, depth+1, path+"."+key)
		if err != nil {
			return nil, err
		}
		dst[canonicalKey] = normalized
	}
	return dst, nil
}

// normalizeConfigValue 递归规范化 JSON 中的对象键，数组顺序和标量值保持不变。
func normalizeConfigValue(value interface{}, depth int, path string) (interface{}, error) {
	switch typed := value.(type) {
	case map[string]interface{}:
		return normalizeConfigMap(typed, depth, path)
	case []interface{}:
		if depth > maxConfigJSONDepth {
			return nil, fmt.Errorf("%w at %s", ErrConfigJSONTooDeep, path)
		}
		result := make([]interface{}, len(typed))
		for index, item := range typed {
			normalized, err := normalizeConfigValue(item, depth+1, fmt.Sprintf("%s[%d]", path, index))
			if err != nil {
				return nil, err
			}
			result[index] = normalized
		}
		return result, nil
	default:
		return value, nil
	}
}

// Get 使用点路径读取配置；map 和 slice 始终在锁内生成递归快照。
func (c *Config) Get(name string, def ...interface{}) interface{} {
	rawName := strings.TrimSpace(name)
	if rawName == "" {
		c.lock.RLock()
		value := deepCopyValue(c.config)
		c.lock.RUnlock()
		return value
	}
	name, ok := normalizeConfigPath(rawName)
	if !ok {
		if len(def) > 0 {
			return def[0]
		}
		return nil
	}

	c.lock.RLock()
	if !strings.Contains(name, ".") {
		if v, exists := c.config[name]; exists {
			value := deepCopyValue(v)
			c.lock.RUnlock()
			return value
		}
		c.lock.RUnlock()
		if len(def) > 0 {
			return def[0]
		}
		return nil
	}
	if cached, exists := c.lookupCache[name]; exists {
		value := resolveLookupValue(cached, def...)
		c.lock.RUnlock()
		return value
	}
	c.lock.RUnlock()

	c.lock.Lock()
	defer c.lock.Unlock()
	c.ensureInitializedLocked()

	if cached, exists := c.lookupCache[name]; exists {
		return resolveLookupValue(cached, def...)
	}

	parts := c.getPathPartsLocked(name)
	value, found := c.resolvePathLocked(parts)
	c.storeLookupCacheLocked(name, configLookupCacheEntry{
		value: value,
		found: found,
	})
	return resolveLookupValue(c.lookupCache[name], def...)
}

// GetString 安全读取字符串配置，类型不符或不存在时返回默认值。
func (c *Config) GetString(name string, def ...string) string {
	fallback := ""
	if len(def) > 0 {
		fallback = def[0]
	}
	if v, ok := c.Get(name).(string); ok {
		return v
	}
	return fallback
}

// GetBool 安全读取布尔配置，兼容 true、false、1 和 0 的字符串写法。
func (c *Config) GetBool(name string, def ...bool) bool {
	fallback := false
	if len(def) > 0 {
		fallback = def[0]
	}
	value, err := c.GetBoolStrict(name)
	if err != nil || !c.Has(name) {
		return fallback
	}
	return value
}

// GetBoolStrict 读取布尔配置并报告非法类型或非法字符串。
func (c *Config) GetBoolStrict(name string) (bool, error) {
	if !c.Has(name) {
		return false, nil
	}
	switch value := c.Get(name).(type) {
	case bool:
		return value, nil
	case nil:
		return false, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "true", "1":
			return true, nil
		case "false", "0":
			return false, nil
		default:
			return false, fmt.Errorf("config %s must be a boolean", name)
		}
	default:
		return false, fmt.Errorf("config %s must be a boolean", name)
	}
}

// GetInt 安全读取整数配置，非法类型、溢出或小数会返回默认值。
func (c *Config) GetInt(name string, def ...int) int {
	fallback := 0
	if len(def) > 0 {
		fallback = def[0]
	}
	if !c.Has(name) {
		return fallback
	}
	value, err := c.GetIntStrict(name)
	if err != nil {
		return fallback
	}
	return value
}

// GetIntStrict 读取整数配置并检查范围、NaN、无穷大和小数。
func (c *Config) GetIntStrict(name string) (int, error) {
	if !c.Has(name) {
		return 0, nil
	}
	value := c.Get(name)
	if value == nil {
		return 0, nil
	}
	converted, ok := configValueToInt(value)
	if !ok {
		return 0, fmt.Errorf("config %s must be an integer", name)
	}
	return converted, nil
}

// configValueToInt 将常见整数类型安全转换为 int。
func configValueToInt(value interface{}) (int, bool) {
	minimum := int64(-int64(^uint(0)>>1) - 1)
	maximum := int64(^uint(0) >> 1)
	maximumUnsigned := uint64(^uint(0) >> 1)
	if number, ok := value.(json.Number); ok {
		return configNumberToInt(number, minimum, maximum)
	}
	reflected := reflect.ValueOf(value)

	switch reflected.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		integer := reflected.Int()
		if integer < minimum || integer > maximum {
			return 0, false
		}
		return int(integer), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		unsigned := reflected.Uint()
		if unsigned > maximumUnsigned {
			return 0, false
		}
		return int(unsigned), true
	case reflect.Float32, reflect.Float64:
		floating := reflected.Float()
		if math.IsNaN(floating) || math.IsInf(floating, 0) || math.Trunc(floating) != floating || floating < float64(minimum) || floating >= float64(maximum)+1 {
			return 0, false
		}
		return int(floating), true
	default:
		return 0, false
	}
}

// configNumberToInt 使用精确有理数解析 JSON 数字，避免大整数经过 float64 舍入。
func configNumberToInt(number json.Number, minimum, maximum int64) (int, bool) {
	rational, ok := new(big.Rat).SetString(number.String())
	if !ok || !rational.IsInt() || !rational.Num().IsInt64() {
		return 0, false
	}
	integer := rational.Num().Int64()
	if integer < minimum || integer > maximum {
		return 0, false
	}
	return int(integer), true
}

// GetMap 安全读取 map 配置并返回独立快照；类型不符时返回非 nil 空 map。
func (c *Config) GetMap(name string) map[string]interface{} {
	if v, ok := c.Get(name).(map[string]interface{}); ok {
		return v
	}
	return make(map[string]interface{})
}

// GetMapCopy 保留兼容名称；GetMap 已经具备同样的递归快照语义。
func (c *Config) GetMapCopy(name string) map[string]interface{} {
	return c.GetMap(name)
}

// deepCopyMap 递归深拷贝配置 map，隔离嵌套 map、slice 和 array。
func deepCopyMap(src map[string]interface{}) map[string]interface{} {
	dst := make(map[string]interface{}, len(src))
	for key, value := range src {
		dst[key] = deepCopyValue(value)
	}
	return dst
}

// deepCopyValue 深拷贝配置中的常见 JSON 值类型；标量值直接返回。
func deepCopyValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return deepCopyMap(typed)
	case map[string]string:
		cloned := make(map[string]string, len(typed))
		for key, item := range typed {
			cloned[key] = item
		}
		return cloned
	case []interface{}:
		cloned := make([]interface{}, len(typed))
		for index, item := range typed {
			cloned[index] = deepCopyValue(item)
		}
		return cloned
	case []string:
		return append([]string(nil), typed...)
	case []int:
		return append([]int(nil), typed...)
	case []int64:
		return append([]int64(nil), typed...)
	case []float64:
		return append([]float64(nil), typed...)
	case []bool:
		return append([]bool(nil), typed...)
	default:
		cloned := deepCopyCollection(reflect.ValueOf(value))
		if !cloned.IsValid() {
			return nil
		}
		return cloned.Interface()
	}
}

// deepCopyCollection 保留强类型集合的原始类型并递归复制 map、slice 和 array 成员。
func deepCopyCollection(value reflect.Value) reflect.Value {
	if !value.IsValid() {
		return value
	}

	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := deepCopyCollection(value.Elem())
		result := reflect.New(value.Type()).Elem()
		result.Set(cloned)
		return result
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		result := reflect.MakeMapWithSize(value.Type(), value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			result.SetMapIndex(iterator.Key(), deepCopyCollection(iterator.Value()))
		}
		return result
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		result := reflect.MakeSlice(value.Type(), value.Len(), value.Cap())
		for index := 0; index < value.Len(); index++ {
			result.Index(index).Set(deepCopyCollection(value.Index(index)))
		}
		return result
	case reflect.Array:
		result := reflect.New(value.Type()).Elem()
		for index := 0; index < value.Len(); index++ {
			result.Index(index).Set(deepCopyCollection(value.Index(index)))
		}
		return result
	default:
		return value
	}
}

// Set 设置一个点路径配置值，并返回路径或输入值校验错误。
func (c *Config) Set(name string, value interface{}) error {
	name, ok := normalizeConfigPath(name)
	if !ok {
		return ErrConfigInvalidPath
	}
	normalized, err := normalizeConfigInputValue(value, 0, "$")
	if err != nil {
		return err
	}

	c.lock.Lock()
	defer c.lock.Unlock()
	c.ensureInitializedLocked()

	if !strings.Contains(name, ".") {
		c.config[name] = normalized
		c.invalidateLookupCacheLocked()
		return nil
	}

	parts := strings.Split(name, ".")
	current := c.config
	for index, part := range parts {
		if index == len(parts)-1 {
			current[part] = normalized
			c.invalidateLookupCacheLocked()
			return nil
		}

		if nested, exists := current[part]; exists {
			if nestedMap, mapOK := nested.(map[string]interface{}); mapOK {
				current = nestedMap
				continue
			}
		}
		replacement := make(map[string]interface{})
		current[part] = replacement
		current = replacement
	}
	return nil
}

// Has 检查配置路径是否存在，即使该路径显式配置为 nil 也返回 true。
func (c *Config) Has(name string) bool {
	rawName := strings.TrimSpace(name)
	if rawName == "" {
		c.lock.RLock()
		defer c.lock.RUnlock()
		return len(c.config) > 0
	}
	name, ok := normalizeConfigPath(rawName)
	if !ok {
		return false
	}

	c.lock.RLock()
	defer c.lock.RUnlock()
	_, found := c.resolvePathLocked(strings.Split(name, "."))
	return found
}

// LoadAll 原子加载目录中的全部 JSON 配置文件。
func (c *Config) LoadAll(dir string) error {
	root, err := filepath.Abs(filepath.Clean(dir))
	if err != nil {
		return err
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s", ErrConfigSymlinkNotAllowed, root)
	}
	if !rootInfo.IsDir() {
		return fmt.Errorf("config path is not a directory: %s", root)
	}

	candidates := make([]configFileCandidate, 0)
	namespaces := make(map[string]string)
	var totalBytes int64
	err = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s", ErrConfigSymlinkNotAllowed, path)
		}
		if info.IsDir() {
			if path != root && configDirectoryDepth(root, path) > maxConfigDirectoryDepth {
				return fmt.Errorf("%w: %s", ErrConfigDirectoryTooDeep, path)
			}
			return nil
		}
		if !info.Mode().IsRegular() || !strings.EqualFold(filepath.Ext(path), ".json") {
			return nil
		}
		if len(candidates) >= maxConfigFileCount {
			return ErrConfigFileCountExceeded
		}
		if info.Size() > maxConfigFileBytes {
			return fmt.Errorf("%w: %s", ErrConfigFileTooLarge, path)
		}
		totalBytes += info.Size()
		if totalBytes > maxConfigTotalBytes {
			return fmt.Errorf("%w: %s", ErrConfigTotalTooLarge, path)
		}

		filename := filepath.Base(path)
		rawName := strings.TrimSuffix(filename, filepath.Ext(filename))
		if strings.TrimSpace(rawName) == "" {
			return fmt.Errorf("%w: %q", ErrConfigInvalidNamespace, filename)
		}
		name, namespaceErr := normalizeConfigNamespace(rawName)
		if namespaceErr != nil {
			return fmt.Errorf("%w: %s", namespaceErr, path)
		}
		if previous, exists := namespaces[name]; exists {
			return fmt.Errorf("%w: %s and %s", ErrConfigDuplicateNamespace, previous, path)
		}
		namespaces[name] = path
		candidates = append(candidates, configFileCandidate{path: path, name: name})
		return nil
	})
	if err != nil {
		return err
	}

	sort.Slice(candidates, func(left, right int) bool {
		return candidates[left].path < candidates[right].path
	})
	loaded := make([]map[string]interface{}, len(candidates))
	for index, candidate := range candidates {
		loaded[index], err = loadConfigFile(candidate.path)
		if err != nil {
			return fmt.Errorf("load config %s failed: %w", candidate.path, err)
		}
	}

	c.lock.Lock()
	defer c.lock.Unlock()
	c.ensureInitializedLocked()
	working := deepCopyMap(c.config)
	for index, candidate := range candidates {
		existing, ok := working[candidate.name].(map[string]interface{})
		if !ok {
			existing = make(map[string]interface{})
		}
		mergeConfigMap(existing, loaded[index])
		working[candidate.name] = existing
	}
	c.config = working
	c.invalidateLookupCacheLocked()
	return nil
}

// configDirectoryDepth 返回目录相对于配置根目录的层数。
func configDirectoryDepth(root, path string) int {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." {
		return 0
	}
	depth := 0
	for _, part := range strings.Split(relative, string(os.PathSeparator)) {
		if part != "" && part != "." {
			depth++
		}
	}
	return depth
}

// normalizeConfigPath 统一点路径大小写并拒绝空路径段。
func normalizeConfigPath(name string) (string, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return "", false
	}
	parts := strings.Split(name, ".")
	for index, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return "", false
		}
		parts[index] = part
	}
	return strings.Join(parts, "."), true
}

// normalizeConfigNamespace 统一命名空间大小写，并拒绝会破坏点路径语义的字符。
func normalizeConfigNamespace(name string) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return "", nil
	}
	if strings.ContainsAny(name, ".\\/") {
		return "", fmt.Errorf("%w: %q", ErrConfigInvalidNamespace, name)
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return "", fmt.Errorf("%w: %q", ErrConfigInvalidNamespace, name)
		}
	}
	return name, nil
}

// normalizeConfigInputValue 规范化 Set 写入的动态 map，并为调用方隔离嵌套集合。
func normalizeConfigInputValue(value interface{}, depth int, path string) (interface{}, error) {
	switch typed := value.(type) {
	case map[string]interface{}:
		if depth > maxConfigJSONDepth {
			return nil, fmt.Errorf("%w at %s", ErrConfigJSONTooDeep, path)
		}
		result := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			canonicalKey := strings.ToLower(key)
			if _, exists := result[canonicalKey]; exists {
				return nil, fmt.Errorf("%w at %s.%s", ErrConfigDuplicateKey, path, key)
			}
			normalized, err := normalizeConfigInputValue(item, depth+1, path+"."+key)
			if err != nil {
				return nil, err
			}
			result[canonicalKey] = normalized
		}
		return result, nil
	case []interface{}:
		if depth > maxConfigJSONDepth {
			return nil, fmt.Errorf("%w at %s", ErrConfigJSONTooDeep, path)
		}
		result := make([]interface{}, len(typed))
		for index, item := range typed {
			normalized, err := normalizeConfigInputValue(item, depth+1, fmt.Sprintf("%s[%d]", path, index))
			if err != nil {
				return nil, err
			}
			result[index] = normalized
		}
		return result, nil
	default:
		return deepCopyValue(value), nil
	}
}

// ensureInitializedLocked 保证 Config 零值在持有写锁时也具备完整内部存储。
func (c *Config) ensureInitializedLocked() {
	if c.config == nil {
		c.config = make(map[string]interface{})
	}
	if c.lookupCache == nil {
		c.lookupCache = make(map[string]configLookupCacheEntry)
	}
	if c.pathCache == nil {
		c.pathCache = make(map[string][]string)
	}
}

// getPathPartsLocked 返回点路径的拆分结果，并缓存热点路径的分段信息。
func (c *Config) getPathPartsLocked(name string) []string {
	if parts, ok := c.pathCache[name]; ok {
		return parts
	}

	parts := strings.Split(name, ".")
	if len(c.pathKeys) >= maxConfigCacheEntries {
		oldest := c.pathKeys[0]
		delete(c.pathCache, oldest)
		copy(c.pathKeys, c.pathKeys[1:])
		c.pathKeys[len(c.pathKeys)-1] = ""
		c.pathKeys = c.pathKeys[:len(c.pathKeys)-1]
	}
	c.pathCache[name] = parts
	c.pathKeys = append(c.pathKeys, name)
	return parts
}

// storeLookupCacheLocked 写入点路径解析结果，并淘汰最早条目以保持缓存有界。
func (c *Config) storeLookupCacheLocked(name string, entry configLookupCacheEntry) {
	c.ensureInitializedLocked()
	if _, exists := c.lookupCache[name]; !exists {
		if len(c.lookupKeys) >= maxConfigCacheEntries {
			oldest := c.lookupKeys[0]
			delete(c.lookupCache, oldest)
			copy(c.lookupKeys, c.lookupKeys[1:])
			c.lookupKeys[len(c.lookupKeys)-1] = ""
			c.lookupKeys = c.lookupKeys[:len(c.lookupKeys)-1]
		}
		c.lookupKeys = append(c.lookupKeys, name)
	}
	c.lookupCache[name] = entry
}

// resolvePathLocked 逐层解析点路径，调用方必须持有读锁或写锁。
func (c *Config) resolvePathLocked(parts []string) (interface{}, bool) {
	var current interface{} = c.config
	for _, part := range parts {
		m, ok := current.(map[string]interface{})
		if !ok {
			return nil, false
		}

		next, ok := m[part]
		if !ok {
			return nil, false
		}
		current = next
	}
	return current, true
}

// invalidateLookupCacheLocked 在配置发生变化后清空旧查询缓存。
func (c *Config) invalidateLookupCacheLocked() {
	c.lookupCache = make(map[string]configLookupCacheEntry)
	c.lookupKeys = nil
}

// resolveLookupValue 根据缓存命中结果返回配置值或默认值。
func resolveLookupValue(entry configLookupCacheEntry, def ...interface{}) interface{} {
	if entry.found {
		return deepCopyValue(entry.value)
	}
	if len(def) > 0 {
		return def[0]
	}
	return nil
}
