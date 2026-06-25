package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// Get gets a config value with dot notation.
//
// 注意：返回的若是 map/slice，为内部配置的引用（出于性能不做拷贝）。
// 调用方必须将其视为只读，禁止直接修改；如需可变副本请使用 GetMapCopy。
// 所有写入必须经由 Set（持写锁并失效缓存）。
func (c *Config) Get(name string, def ...interface{}) interface{} {
	name = strings.ToLower(strings.TrimSpace(name))

	c.lock.RLock()
	if name == "" {
		defer c.lock.RUnlock()
		return c.config
	}

	if !strings.Contains(name, ".") {
		defer c.lock.RUnlock()
		if v, ok := c.config[name]; ok {
			return v
		}
		if len(def) > 0 {
			return def[0]
		}
		return nil
	}
	if cached, ok := c.lookupCache[name]; ok {
		c.lock.RUnlock()
		return resolveLookupValue(cached, def...)
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

// GetMap 安全读取 map 配置，类型不符或不存在时返回空 map（非 nil），避免调用方断言 panic。
// 返回的是内部配置引用，调用方必须只读；需要修改请用 GetMapCopy。
func (c *Config) GetMap(name string) map[string]interface{} {
	if v, ok := c.Get(name).(map[string]interface{}); ok {
		return v
	}
	return make(map[string]interface{})
}

// GetMapCopy 返回 map 配置的深拷贝，供需要在本地修改而不污染共享配置的调用方使用。
// 仅在确需可变副本时调用，普通读取请用 GetMap 以避免不必要的拷贝开销。
func (c *Config) GetMapCopy(name string) map[string]interface{} {
	return deepCopyMap(c.GetMap(name))
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
	case []interface{}:
		cloned := make([]interface{}, len(typed))
		for index, item := range typed {
			cloned[index] = deepCopyValue(item)
		}
		return cloned
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
		c.config[name] = value
		c.invalidateLookupCacheLocked()
		return
	}

	parts := strings.Split(name, ".")
	var current map[string]interface{} = c.config
	for i, part := range parts {
		if i == len(parts)-1 {
			current[part] = value
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
		return entry.value
	}
	if len(def) > 0 {
		return def[0]
	}
	return nil
}
