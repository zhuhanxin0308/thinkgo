package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
)

// Config manager
type Config struct {
	config      map[string]interface{}
	lookupCache map[string]configLookupCacheEntry
	pathCache   map[string][]string
	lock        sync.RWMutex
}

// configLookupCacheEntry 缓存点路径查询结果，避免热点配置重复拆分与逐层遍历。
type configLookupCacheEntry struct {
	value interface{}
	found bool
}

// NewConfig creates a new Config manager
func NewConfig() *Config {
	return &Config{
		config:      make(map[string]interface{}),
		lookupCache: make(map[string]configLookupCacheEntry),
		pathCache:   make(map[string][]string),
	}
}

// Load loads a config file into a namespace
func (c *Config) Load(file string, name string) error {
	c.lock.Lock()
	defer c.lock.Unlock()

	content, err := os.ReadFile(file)
	if err != nil {
		return err
	}

	var data map[string]interface{}
	// Assume JSON for now
	if err := json.Unmarshal(content, &data); err != nil {
		return err
	}

	if name != "" {
		name = strings.ToLower(name)
		if _, ok := c.config[name]; !ok {
			c.config[name] = make(map[string]interface{})
		}
		// Merge or overwrite? ThinkPHP merges if exists.
		// For simplicity, let's overwrite or merge at top level.
		if existing, ok := c.config[name].(map[string]interface{}); ok {
			for k, v := range data {
				existing[k] = v
			}
			c.config[name] = existing
		} else {
			c.config[name] = data
		}
	} else {
		for k, v := range data {
			c.config[strings.ToLower(k)] = v
		}
	}
	c.invalidateLookupCacheLocked()
	return nil
}

// Get 使用点路径读取配置；map 和 slice 始终在锁内生成递归快照。
// 调用方可以安全修改返回值，所有共享配置写入仍必须通过 Set 完成。
func (c *Config) Get(name string, def ...interface{}) interface{} {
	name = strings.ToLower(strings.TrimSpace(name))

	c.lock.RLock()
	if name == "" {
		value := deepCopyValue(c.config)
		c.lock.RUnlock()
		return value
	}

	if !strings.Contains(name, ".") {
		if v, ok := c.config[name]; ok {
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
	if cached, ok := c.lookupCache[name]; ok {
		value := resolveLookupValue(cached, def...)
		c.lock.RUnlock()
		return value
	}
	c.lock.RUnlock()

	c.lock.Lock()
	defer c.lock.Unlock()

	if cached, ok := c.lookupCache[name]; ok {
		return resolveLookupValue(cached, def...)
	}

	parts := c.getPathPartsLocked(name)
	value, found := c.resolvePathLocked(parts)
	c.lookupCache[name] = configLookupCacheEntry{
		value: value,
		found: found,
	}
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

// GetBool 安全读取布尔配置，兼容字符串 "true"/"1" 写法，类型不符时返回默认值。
func (c *Config) GetBool(name string, def ...bool) bool {
	fallback := false
	if len(def) > 0 {
		fallback = def[0]
	}
	switch v := c.Get(name).(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1"
	case nil:
		return fallback
	default:
		return fallback
	}
}

// GetInt 安全读取整型配置，兼容 JSON float64，类型不符时返回默认值。
func (c *Config) GetInt(name string, def ...int) int {
	fallback := 0
	if len(def) > 0 {
		fallback = def[0]
	}
	switch v := c.Get(name).(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return fallback
	}
}

// GetMap 安全读取 map 配置并返回独立快照；类型不符时返回非 nil 空 map。
func (c *Config) GetMap(name string) map[string]interface{} {
	if v, ok := c.Get(name).(map[string]interface{}); ok {
		return v
	}
	return make(map[string]interface{})
}

// GetMapCopy 保留兼容名称；GetMap 已经具备相同的递归快照语义。
func (c *Config) GetMapCopy(name string) map[string]interface{} {
	return c.GetMap(name)
}

// deepCopyMap 递归深拷贝配置 map，隔离嵌套 map/slice，避免修改副本影响内部配置。
func deepCopyMap(src map[string]interface{}) map[string]interface{} {
	dst := make(map[string]interface{}, len(src))
	for key, value := range src {
		dst[key] = deepCopyValue(value)
	}
	return dst
}

// deepCopyValue 深拷贝配置中的常见 JSON 值类型；标量直接返回。
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

// deepCopyCollection 保留强类型集合的原始类型并递归复制其 map、slice 和 array 成员。
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

// Set sets a config value
func (c *Config) Set(name string, value interface{}) {
	c.lock.Lock()
	defer c.lock.Unlock()

	name = strings.ToLower(name)
	if !strings.Contains(name, ".") {
		c.config[name] = deepCopyValue(value)
		c.invalidateLookupCacheLocked()
		return
	}

	parts := strings.Split(name, ".")
	var current map[string]interface{} = c.config
	for i, part := range parts {
		if i == len(parts)-1 {
			current[part] = deepCopyValue(value)
			c.invalidateLookupCacheLocked()
			return
		}

		if v, ok := current[part]; ok {
			if m, ok := v.(map[string]interface{}); ok {
				current = m
			} else {
				// Overwrite non-map value with map to continue
				m := make(map[string]interface{})
				current[part] = m
				current = m
			}
		} else {
			m := make(map[string]interface{})
			current[part] = m
			current = m
		}
	}
}

// Has checks if config exists
func (c *Config) Has(name string) bool {
	return c.Get(name) != nil
}

// LoadAll loads all config files from a directory
func (c *Config) LoadAll(dir string) error {
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(path, ".json") {
			filename := filepath.Base(path)
			name := strings.TrimSuffix(filename, filepath.Ext(filename))
			if loadErr := c.Load(path, name); loadErr != nil {
				return fmt.Errorf("load config %s failed: %w", path, loadErr)
			}
		}
		return nil
	})
}

// getPathPartsLocked 返回点路径的拆分结果，并缓存热点路径的分段信息。
func (c *Config) getPathPartsLocked(name string) []string {
	if parts, ok := c.pathCache[name]; ok {
		return parts
	}

	parts := strings.Split(name, ".")
	c.pathCache[name] = parts
	return parts
}

// resolvePathLocked 逐层解析点路径，调用方需确保已持有读锁或写锁。
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

// invalidateLookupCacheLocked 在配置发生变化后清空旧查询缓存，避免读到过期值。
func (c *Config) invalidateLookupCacheLocked() {
	c.lookupCache = make(map[string]configLookupCacheEntry)
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
