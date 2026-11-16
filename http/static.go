package http

import (
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/internal/regularfile"
)

const (
	maxSPAIndexBytes          int64 = 2 << 20
	maxStaticURLPathLength          = 2048
	spaIndexCacheTTL                = 250 * time.Millisecond
	staticMissCacheTTL              = 250 * time.Millisecond
	maxStaticMissCacheEntries       = 256
)

type staticPrefixEntry struct {
	at      time.Time
	probeID uint64
	missing bool
}

// servePublicFile 通过已验证的文件句柄响应并返回已发送状态，非静态请求返回零。
func (h *Http) servePublicFile(writer http.ResponseWriter, req *http.Request, urlPath string) int {
	if req == nil || req.URL == nil || containsEncodedPathSeparator(req.URL.EscapedPath()) {
		return 0
	}
	file, info, ok := h.openPublicFile(urlPath)
	if !ok {
		return 0
	}
	defer file.Close()
	statusWriter, tracked := writer.(interface{ Status() int })
	if !tracked {
		// 直接调用 Run 可能提供普通 ResponseWriter，需要在此捕获条件响应状态。
		tracker := newStatusTrackingResponseWriter(writer)
		writer, statusWriter = tracker, tracker
	}
	http.ServeContent(writer, req, info.Name(), info.ModTime(), file)
	return statusWriter.Status()
}

func containsEncodedPathSeparator(escapedPath string) bool {
	lowerPath := strings.ToLower(escapedPath)
	return strings.Contains(lowerPath, "%2f") || strings.Contains(lowerPath, "%5c")
}

// spaIndexContent 返回受 public 根目录约束的不可变 SPA 入口快照。
func (h *Http) spaIndexContent() (string, bool) {
	now := time.Now()
	h.spaIndexMu.RLock()
	if !h.spaIndexAt.IsZero() && now.Sub(h.spaIndexAt) < spaIndexCacheTTL {
		content := h.spaIndexCache
		cached := h.spaIndexCached
		h.spaIndexMu.RUnlock()
		return content, cached
	}
	h.spaIndexMu.RUnlock()

	h.spaIndexMu.Lock()
	defer h.spaIndexMu.Unlock()
	if !h.spaIndexAt.IsZero() && now.Sub(h.spaIndexAt) < spaIndexCacheTTL {
		return h.spaIndexCache, h.spaIndexCached
	}
	// SPA 缓存窗口结束即应重新探测入口，不能再受通用缺失前缀缓存延迟。
	h.forgetStaticPrefixMiss("/index.html")
	file, info, ok := h.openPublicFile("/index.html")
	if !ok || info.Size() < 0 || info.Size() > maxSPAIndexBytes {
		if file != nil {
			_ = file.Close()
		}
		h.spaIndexCache = ""
		h.spaIndexCached = false
		h.spaIndexAt = now
		return "", false
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxSPAIndexBytes+1))
	if err != nil || int64(len(content)) > maxSPAIndexBytes {
		h.spaIndexCache = ""
		h.spaIndexCached = false
		h.spaIndexAt = now
		return "", false
	}
	h.spaIndexCache = string(content)
	h.spaIndexCached = true
	h.spaIndexAt = now
	return h.spaIndexCache, true
}

func (h *Http) openPublicFile(urlPath string) (*os.File, os.FileInfo, bool) {
	if h == nil || h.app == nil || urlPath == "" || len(urlPath) > maxStaticURLPathLength || strings.ContainsAny(urlPath, "\\\x00\r\n") {
		return nil, nil, false
	}
	cleanPath := path.Clean("/" + urlPath)
	if cleanPath != urlPath || cleanPath == "/" {
		return nil, nil, false
	}
	if !publicStaticRelativePath(strings.TrimPrefix(cleanPath, "/")) {
		return nil, nil, false
	}
	prefix := staticPathPrefix(cleanPath)
	probeID, cached := h.reserveStaticPrefixProbe(prefix, time.Now())
	if cached {
		return nil, nil, false
	}
	missing := false
	defer func() { h.finishStaticPrefixProbe(prefix, probeID, missing, time.Now()) }()

	publicPath := h.app.ProjectPublicPath()
	publicAbsolute, err := filepath.Abs(publicPath)
	if err != nil {
		return nil, nil, false
	}
	targetCandidate := filepath.Join(publicAbsolute, filepath.FromSlash(strings.TrimPrefix(cleanPath, "/")))
	// 绝大多数 API 路径并不对应静态文件。先尝试打开候选路径，缺失时无需
	// 解析 public 根目录和目标路径的符号链接；成功打开后仍执行完整校验，
	// 并在返回前确认句柄与已校验目标是同一文件，避免降低防穿越保护。
	file, info, err := regularfile.Open(targetCandidate)
	if err != nil {
		if probeID != 0 && os.IsNotExist(err) {
			// 子文件缺失时，只有首级路径也不存在才能发布负缓存。
			_, err := os.Lstat(filepath.Join(publicAbsolute, filepath.FromSlash(prefix)))
			missing = os.IsNotExist(err)
		}
		return nil, nil, false
	}
	publicReal, err := filepath.EvalSymlinks(publicAbsolute)
	if err != nil {
		_ = file.Close()
		return nil, nil, false
	}
	targetReal, err := filepath.EvalSymlinks(targetCandidate)
	if err != nil || !publicStaticTarget(publicReal, targetReal) {
		_ = file.Close()
		return nil, nil, false
	}
	verifiedReal, verifyErr := filepath.EvalSymlinks(targetCandidate)
	verifiedInfo, statErr := os.Stat(verifiedReal)
	if err != nil || verifyErr != nil || statErr != nil || !publicStaticTarget(publicReal, verifiedReal) || !os.SameFile(info, verifiedInfo) || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, false
	}
	return file, info, true
}

// reserveStaticPrefixProbe 为可能发布的结果保留编号。已知缺失的条目只允许一次过期刷新。
func (h *Http) reserveStaticPrefixProbe(prefix string, now time.Time) (uint64, bool) {
	h.staticMissMu.Lock()
	defer h.staticMissMu.Unlock()
	entry, exists := h.staticMisses[prefix]
	if entry.missing && now.Sub(entry.at) < staticMissCacheTTL {
		return 0, true
	}
	if !exists && len(h.staticMisses) >= maxStaticMissCacheEntries {
		for key, candidate := range h.staticMisses {
			if now.Sub(candidate.at) >= staticMissCacheTTL {
				delete(h.staticMisses, key)
			}
		}
		if len(h.staticMisses) >= maxStaticMissCacheEntries {
			return 0, false
		}
	}
	if h.staticMisses == nil {
		h.staticMisses = make(map[string]staticPrefixEntry)
	}
	h.staticProbeID++
	h.staticMisses[prefix] = staticPrefixEntry{at: now, probeID: h.staticProbeID, missing: entry.missing}
	return h.staticProbeID, false
}

func (h *Http) finishStaticPrefixProbe(prefix string, probeID uint64, missing bool, now time.Time) {
	if probeID == 0 {
		return
	}
	h.staticMissMu.Lock()
	defer h.staticMissMu.Unlock()
	entry := h.staticMisses[prefix]
	// 只有当前探测可以发布。更新的结果、逐出和清理都会使旧编号失效。
	if entry.probeID != probeID {
		return
	}
	if missing && now.Sub(entry.at) < staticMissCacheTTL {
		h.staticMisses[prefix] = staticPrefixEntry{at: entry.at, missing: true}
		return
	}
	delete(h.staticMisses, prefix)
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

// publicStaticTarget applies the same visibility policy after resolving symlinks.
func publicStaticTarget(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && !filepath.IsAbs(relative) && publicStaticRelativePath(filepath.ToSlash(relative))
}

// publicStaticRelativePath rejects hidden segments except the root well-known namespace.
func publicStaticRelativePath(relative string) bool {
	segments := strings.Split(relative, "/")
	for index, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
		if strings.HasPrefix(segment, ".") && !(index == 0 && len(segments) > 1 && segment == ".well-known") {
			return false
		}
	}
	return true
}
