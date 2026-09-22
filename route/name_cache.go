package route

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
)

const (
	nameCacheVersion = 1
	// NameCacheFilename 是应用运行目录中的命名路由优化产物。
	NameCacheFilename = "route_names.json"
	// MaxNameCacheBytes 限制可读取的命名路由优化文件大小。
	MaxNameCacheBytes = 16 << 20
)

type namedRouteCache struct {
	Version        int               `json:"version"`
	DefaultPattern string            `json:"default_pattern"`
	Entries        []namedRouteEntry `json:"entries"`
}

type namedRouteEntry struct {
	Name      string            `json:"name"`
	Path      string            `json:"path"`
	Extension string            `json:"extension,omitempty"`
	Patterns  map[string]string `json:"patterns,omitempty"`
}

// ExportNameCache 导出命名路由的 URL 生成信息，对应 ThinkPHP optimize:route 的命名路由缓存。
// 处理器和中间件仍由 Go 编译程序提供，不尝试序列化闭包或可执行对象。
func (router *Router) ExportNameCache() ([]byte, error) {
	if router == nil {
		return nil, ErrInvalidRoute
	}
	if err := router.Freeze(); err != nil {
		return nil, err
	}
	router.mu.RLock()
	defer router.mu.RUnlock()
	cache := namedRouteCache{Version: nameCacheVersion, DefaultPattern: router.defaultPattern, Entries: make([]namedRouteEntry, 0, len(router.namedRoutes))}
	for name, registered := range router.namedRoutes {
		patterns := make(map[string]string, len(registered.patterns))
		for name, pattern := range registered.patterns {
			patterns[name] = pattern
		}
		cache.Entries = append(cache.Entries, namedRouteEntry{Name: name, Path: registered.path, Extension: registered.ext, Patterns: patterns})
	}
	sort.Slice(cache.Entries, func(left, right int) bool { return cache.Entries[left].Name < cache.Entries[right].Name })
	content, err := json.MarshalIndent(cache, "", "    ")
	if err != nil {
		return nil, err
	}
	if len(content) >= MaxNameCacheBytes {
		return nil, fmt.Errorf("命名路由缓存超过大小限制")
	}
	return append(content, '\n'), nil
}

// LoadNameCache 完整验证缓存后原子替换 URL 元数据；失败不会污染已有路由或缓存。
func (router *Router) LoadNameCache(content []byte) error {
	if router == nil {
		return ErrInvalidRoute
	}
	if len(content) > MaxNameCacheBytes {
		return fmt.Errorf("命名路由缓存超过大小限制")
	}
	var cache namedRouteCache
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cache); err != nil {
		return fmt.Errorf("解析命名路由缓存失败: %w", err)
	}
	if err := decoder.Decode(new(interface{})); err != io.EOF {
		return fmt.Errorf("命名路由缓存存在尾随内容")
	}
	if cache.Version != nameCacheVersion || cache.Entries == nil {
		return fmt.Errorf("命名路由缓存版本或结构不匹配")
	}
	metadataRouter := NewRouter()
	if err := metadataRouter.SetDefaultPattern(cache.DefaultPattern); err != nil {
		return err
	}
	names := make(map[string]*Route, len(cache.Entries))
	for _, entry := range cache.Entries {
		if !isValidRouteName(entry.Name) || names[entry.Name] != nil {
			return fmt.Errorf("命名路由缓存名称非法或重复: %q", entry.Name)
		}
		path, _, err := canonicalRoutePath(entry.Path)
		if err != nil {
			return err
		}
		parts, optional, err := parseRoutePath(path)
		if err != nil || optional > maxOptionalRouteSegments {
			return fmt.Errorf("命名路由缓存路径非法: %q", entry.Path)
		}
		extension, err := normalizeRouteExtension(entry.Extension)
		if err != nil {
			return err
		}
		registered := &Route{router: metadataRouter, path: path, pathParts: parts, ext: extension, patterns: make(map[string]string), compiled: make(map[string]*regexp.Regexp)}
		for name, pattern := range entry.Patterns {
			if err := registered.WithPattern(name, pattern); err != nil {
				return err
			}
		}
		names[entry.Name] = registered
	}
	router.mu.Lock()
	router.cachedNames = names
	router.mu.Unlock()
	return nil
}
