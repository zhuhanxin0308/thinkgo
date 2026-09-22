package http

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Name 设置应用名称并返回当前 Http。
func (h *Http) Name(name string) *Http {
	if h == nil {
		return nil
	}
	h.metadataMu.Lock()
	h.name = name
	h.metadataMu.Unlock()
	return h
}

// GetName 返回当前应用名称，默认值为空。
func (h *Http) GetName() string {
	if h == nil {
		return ""
	}
	h.metadataMu.RLock()
	name := h.name
	h.metadataMu.RUnlock()
	return name
}

// Path 设置当前应用路径并返回当前 Http。
func (h *Http) Path(path string) *Http {
	if h == nil {
		return nil
	}
	normalized := strings.TrimSpace(path)
	if normalized != "" {
		normalized = filepath.Clean(normalized)
	}
	h.metadataMu.Lock()
	h.path = normalized
	h.metadataMu.Unlock()
	return h
}

// GetPath 返回当前应用路径，默认值为空。
func (h *Http) GetPath() string {
	if h == nil {
		return ""
	}
	h.metadataMu.RLock()
	path := h.path
	h.metadataMu.RUnlock()
	return path
}

// GetRoutePath 返回路由定义目录。
func (h *Http) GetRoutePath() string {
	if h == nil {
		return ""
	}
	h.metadataMu.RLock()
	path := h.routePath
	h.metadataMu.RUnlock()
	return path
}

// SetRoutePath 设置路由定义目录。
// Go 路由文件仍由编译期装配，目录用于保持 Http 元数据和生成工具的一致来源。
func (h *Http) SetRoutePath(path string) {
	if h == nil {
		return
	}
	normalized := strings.TrimSpace(path)
	if normalized != "" {
		normalized = filepath.Clean(normalized)
	}
	h.metadataMu.Lock()
	h.routePath = normalized
	h.metadataMu.Unlock()
}

// SetBind 设置是否绑定应用并返回当前 Http；不传参数时默认启用。
func (h *Http) SetBind(bind ...bool) *Http {
	if h == nil {
		return nil
	}
	if len(bind) > 1 {
		panic(fmt.Errorf("SetBind 最多只能指定一个参数"))
	}
	enabled := true
	if len(bind) == 1 {
		enabled = bind[0]
	}
	h.metadataMu.Lock()
	h.bind = enabled
	h.metadataMu.Unlock()
	return h
}

// IsBind 返回是否绑定应用。
func (h *Http) IsBind() bool {
	if h == nil {
		return false
	}
	h.metadataMu.RLock()
	bind := h.bind
	h.metadataMu.RUnlock()
	return bind
}
