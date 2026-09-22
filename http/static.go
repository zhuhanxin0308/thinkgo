package http

import (
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxSPAIndexBytes          int64 = 2 << 20
	maxStaticURLPathLength          = 2048
	spaIndexCacheTTL                = 250 * time.Millisecond
	staticMissCacheTTL              = 250 * time.Millisecond
	maxStaticMissCacheEntries       = 256
)

// servePublicFile 使用已完成真实路径校验的文件句柄响应，避免 ServeFile 再次按不可信路径打开文件。
func (h *Http) servePublicFile(writer http.ResponseWriter, req *http.Request, urlPath string) bool {
	if req == nil || req.URL == nil || containsEncodedPathSeparator(req.URL.EscapedPath()) {
		return false
	}
	file, info, ok := h.openPublicFile(urlPath)
	if !ok {
		return false
	}
	defer file.Close()
	http.ServeContent(writer, req, info.Name(), info.ModTime(), file)
	return true
}

func containsEncodedPathSeparator(escapedPath string) bool {
	lowerPath := strings.ToLower(escapedPath)
	return strings.Contains(lowerPath, "%2f") || strings.Contains(lowerPath, "%5c")
}

// spaIndexContent 读取受 public 根目录约束且大小有限的 SPA 入口。
func (h *Http) spaIndexContent() ([]byte, bool) {
	now := time.Now()
	h.spaIndexMu.RLock()
	if !h.spaIndexAt.IsZero() && now.Sub(h.spaIndexAt) < spaIndexCacheTTL {
		content := append([]byte(nil), h.spaIndexCache...)
		cached := h.spaIndexCached
		h.spaIndexMu.RUnlock()
		return content, cached
	}
	h.spaIndexMu.RUnlock()

	h.spaIndexMu.Lock()
	defer h.spaIndexMu.Unlock()
	if !h.spaIndexAt.IsZero() && now.Sub(h.spaIndexAt) < spaIndexCacheTTL {
		return append([]byte(nil), h.spaIndexCache...), h.spaIndexCached
	}
	// SPA 缓存窗口结束即应重新探测入口，不能再受通用缺失前缀缓存延迟。
	h.forgetStaticPrefixMiss("/index.html")
	file, info, ok := h.openPublicFile("/index.html")
	if !ok || info.Size() < 0 || info.Size() > maxSPAIndexBytes {
		if file != nil {
			_ = file.Close()
		}
		h.spaIndexCache = nil
		h.spaIndexCached = false
		h.spaIndexAt = now
		return nil, false
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxSPAIndexBytes+1))
	if err != nil || int64(len(content)) > maxSPAIndexBytes {
		h.spaIndexCache = nil
		h.spaIndexCached = false
		h.spaIndexAt = now
		return nil, false
	}
	h.spaIndexCache = append(h.spaIndexCache[:0], content...)
	h.spaIndexCached = true
	h.spaIndexAt = now
	return append([]byte(nil), h.spaIndexCache...), true
}

func (h *Http) openPublicFile(urlPath string) (*os.File, os.FileInfo, bool) {
	if h == nil || h.app == nil || urlPath == "" || len(urlPath) > maxStaticURLPathLength || strings.ContainsAny(urlPath, "\\\x00\r\n") {
		return nil, nil, false
	}
	cleanPath := path.Clean("/" + urlPath)
	if cleanPath != urlPath || cleanPath == "/" {
		return nil, nil, false
	}
	for _, segment := range strings.Split(strings.Trim(cleanPath, "/"), "/") {
		if segment == "" || segment == "." || segment == ".." {
			return nil, nil, false
		}
	}
	if h.reserveStaticPrefixProbe(cleanPath, time.Now()) {
		return nil, nil, false
	}

	publicPath := h.app.ProjectPublicPath()
	publicAbsolute, err := filepath.Abs(publicPath)
	if err != nil {
		return nil, nil, false
	}
	targetCandidate := filepath.Join(publicAbsolute, filepath.FromSlash(strings.TrimPrefix(cleanPath, "/")))
	// 绝大多数 API 路径并不对应静态文件。先尝试打开候选路径，缺失时无需
	// 解析 public 根目录和目标路径的符号链接；成功打开后仍执行完整校验，
	// 并在返回前确认句柄与已校验目标是同一文件，避免降低防穿越保护。
	// #nosec G304 -- targetCandidate 由 public 根目录、规范化 URL 和符号链接复核共同生成。
	file, err := os.Open(targetCandidate)
	if err != nil {
		if os.IsNotExist(err) {
			h.rememberStaticPrefixMiss(cleanPath, publicAbsolute, time.Now())
		}
		return nil, nil, false
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, false
	}
	publicReal, err := filepath.EvalSymlinks(publicAbsolute)
	if err != nil {
		_ = file.Close()
		return nil, nil, false
	}
	targetReal, err := filepath.EvalSymlinks(targetCandidate)
	if err != nil || !pathWithinRoot(publicReal, targetReal) {
		_ = file.Close()
		return nil, nil, false
	}
	verifiedReal, verifyErr := filepath.EvalSymlinks(targetCandidate)
	verifiedInfo, statErr := os.Stat(verifiedReal)
	if err != nil || verifyErr != nil || statErr != nil || !pathWithinRoot(publicReal, verifiedReal) || !os.SameFile(info, verifiedInfo) || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, false
	}
	h.forgetStaticPrefixMiss(cleanPath)
	return file, info, true
}

// reserveStaticPrefixProbe 缓存 public 中不存在的首级目录。缓存有效时直接
// 跳过静态探测；缓存到期后仅允许一个请求重新探测，其余并发请求继续命中缓存，
// 避免到期瞬间的文件系统惊群。新建静态目录最多延迟一个缓存窗口后可见。
func (h *Http) reserveStaticPrefixProbe(cleanPath string, now time.Time) bool {
	prefix := staticPathPrefix(cleanPath)
	if h == nil || prefix == "" {
		return false
	}
	h.staticMissMu.Lock()
	defer h.staticMissMu.Unlock()
	at, exists := h.staticMisses[prefix]
	if !exists || at.IsZero() {
		return false
	}
	if now.Sub(at) < staticMissCacheTTL {
		return true
	}
	// 先续期再由当前请求探测，确保其余并发请求不会重复打开同一缺失路径。
	h.staticMisses[prefix] = now
	return false
}

func (h *Http) rememberStaticPrefixMiss(cleanPath, publicAbsolute string, now time.Time) {
	prefix := staticPathPrefix(cleanPath)
	if h == nil || prefix == "" || publicAbsolute == "" {
		return
	}
	if _, err := os.Lstat(filepath.Join(publicAbsolute, filepath.FromSlash(prefix))); !os.IsNotExist(err) {
		return
	}

	h.staticMissMu.Lock()
	defer h.staticMissMu.Unlock()
	if h.staticMisses == nil {
		h.staticMisses = make(map[string]time.Time)
	}
	if len(h.staticMisses) >= maxStaticMissCacheEntries {
		for key, at := range h.staticMisses {
			if now.Sub(at) >= staticMissCacheTTL {
				delete(h.staticMisses, key)
			}
		}
		if len(h.staticMisses) >= maxStaticMissCacheEntries {
			return
		}
	}
	h.staticMisses[prefix] = now
}

func (h *Http) forgetStaticPrefixMiss(cleanPath string) {
	prefix := staticPathPrefix(cleanPath)
	if h == nil || prefix == "" {
		return
	}
	h.staticMissMu.Lock()
	delete(h.staticMisses, prefix)
	h.staticMissMu.Unlock()
}

func staticPathPrefix(cleanPath string) string {
	trimmed := strings.TrimPrefix(cleanPath, "/")
	if trimmed == "" {
		return ""
	}
	if index := strings.IndexByte(trimmed, '/'); index >= 0 {
		return trimmed[:index]
	}
	return trimmed
}

func pathWithinRoot(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || filepath.IsAbs(relative) {
		return false
	}
	return !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}
