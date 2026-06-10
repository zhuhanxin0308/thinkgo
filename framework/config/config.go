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

// Get gets a config value with dot notation
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
