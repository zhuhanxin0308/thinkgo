package context

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	urlpath "path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var requestDomainSpecialSuffix = map[string]struct{}{
	"com": {}, "net": {}, "org": {}, "edu": {}, "gov": {}, "mil": {}, "co": {}, "info": {},
}

// SetRootDomain 设置用于子域名分析的根域名。
func (r *Request) SetRootDomain(domain string) *Request {
	state := r.thinkPHPState()
	if state == nil {
		return r
	}
	state.mu.Lock()
	state.rootDomain = strings.Trim(strings.TrimSpace(domain), ".")
	state.mu.Unlock()
	return r
}

// RootDomain 获取当前根域名，并识别 ThinkPHP 定义的二级特殊后缀。
func (r *Request) RootDomain() string {
	state := r.thinkPHPState()
	if state == nil {
		return ""
	}
	state.mu.RLock()
	configured := state.rootDomain
	state.mu.RUnlock()
	if configured != "" {
		return configured
	}
	host := strings.TrimSuffix(strings.ToLower(r.Host(true)), ".")
	if host == "" || net.ParseIP(host) != nil {
		return host
	}
	parts := strings.Split(host, ".")
	if len(parts) < 2 {
		return host
	}
	start := len(parts) - 2
	if len(parts) > 2 {
		if _, special := requestDomainSpecialSuffix[parts[len(parts)-2]]; special {
			start--
		}
	}
	return strings.Join(parts[start:], ".")
}

// SetSubDomain 设置当前子域名。
func (r *Request) SetSubDomain(domain string) *Request {
	state := r.thinkPHPState()
	if state == nil {
		return r
	}
	state.mu.Lock()
	state.subDomain = strings.Trim(strings.TrimSpace(domain), ".")
	state.mu.Unlock()
	return r
}

// SubDomain 获取当前子域名。
func (r *Request) SubDomain() string {
	state := r.thinkPHPState()
	if state == nil {
		return ""
	}
	state.mu.RLock()
	configured := state.subDomain
	state.mu.RUnlock()
	if configured != "" {
		return configured
	}
	host := strings.TrimSuffix(strings.ToLower(r.Host(true)), ".")
	root := strings.ToLower(r.RootDomain())
	if host == "" || root == "" || host == root || !strings.HasSuffix(host, "."+root) {
		return ""
	}
	return strings.TrimSuffix(host, "."+root)
}

// SetPanDomain 设置当前泛域名匹配值。
func (r *Request) SetPanDomain(domain string) *Request {
	state := r.thinkPHPState()
	if state == nil {
		return r
	}
	state.mu.Lock()
	state.panDomain = domain
	state.mu.Unlock()
	return r
}

// PanDomain 获取当前泛域名匹配值。
func (r *Request) PanDomain() string {
	state := r.thinkPHPState()
	if state == nil {
		return ""
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.panDomain
}

// BaseFile 获取入口脚本路径；普通 Go HTTP 宿主没有脚本入口时返回空字符串。
func (r *Request) BaseFile(complete ...bool) string {
	state := r.thinkPHPState()
	if state == nil {
		return ""
	}
	state.mu.RLock()
	if state.baseFileSet {
		value := state.baseFile
		state.mu.RUnlock()
		return r.completeRequestPath(value, complete)
	}
	state.mu.RUnlock()

	value := r.resolveBaseFile()
	state.mu.Lock()
	if !state.baseFileSet {
		state.baseFile = value
		state.baseFileSet = true
	} else {
		value = state.baseFile
	}
	state.mu.Unlock()
	return r.completeRequestPath(value, complete)
}

// RootUrl 获取不含入口脚本文件名的 URL 根目录。
func (r *Request) RootUrl() string {
	root := r.Root()
	if strings.Contains(root, ".") {
		root = strings.TrimLeft(urlpath.Dir(strings.ReplaceAll(root, "\\", "/")), "/")
	}
	if root == "" || root == "." {
		return ""
	}
	return "/" + strings.TrimLeft(root, "/")
}

// Time 获取请求创建时间；useFloat=true 返回秒级浮点数，否则返回 Unix 秒。
func (r *Request) Time(useFloat ...bool) interface{} {
	state := r.thinkPHPState()
	if state == nil {
		if len(useFloat) > 0 && useFloat[0] {
			return float64(0)
		}
		return int64(0)
	}
	state.mu.RLock()
	createdAt := state.createdAt
	state.mu.RUnlock()
	if len(useFloat) > 0 && useFloat[0] {
		if configured := r.serverValue("REQUEST_TIME_FLOAT"); configured != "" {
			if parsed, err := strconv.ParseFloat(configured, 64); err == nil {
				return parsed
			}
		}
		return float64(createdAt.UnixNano()) / float64(time.Second)
	}
	if configured := r.serverValue("REQUEST_TIME"); configured != "" {
		if parsed, err := strconv.ParseInt(configured, 10, 64); err == nil {
			return parsed
		}
	}
	return createdAt.Unix()
}

// Type 根据 Accept 头按 ThinkPHP 的资源类型表返回首个匹配名称。
func (r *Request) Type() string {
	accept := strings.ToLower(r.headerValue("Accept"))
	if accept == "" {
		return ""
	}
	state := r.thinkPHPState()
	if state == nil {
		return ""
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	// 默认表只读，自定义表由当前请求独占；在读锁内遍历即可避免每次匹配复制全部定义。
	types := state.mimeTypes
	if types == nil {
		types = defaultRequestMIMETypes[:]
	}
	for _, current := range types {
		for _, value := range current.values {
			if strings.Contains(accept, strings.ToLower(value)) {
				return current.name
			}
		}
	}
	return ""
}

// MimeType 合并资源类型定义；支持名称和值，或 map[string]string 两种调用方式。
func (r *Request) MimeType(arguments ...interface{}) *Request {
	state := r.thinkPHPState()
	if state == nil {
		return r
	}
	updates := make([]requestMIMEType, 0)
	if len(arguments) == 1 {
		switch values := arguments[0].(type) {
		case map[string]string:
			for name, value := range values {
				updates = append(updates, requestMIMEType{name: name, values: splitMIMEValues(value)})
			}
		case map[string]interface{}:
			for name, value := range values {
				text, ok := value.(string)
				if ok {
					updates = append(updates, requestMIMEType{name: name, values: splitMIMEValues(text)})
				}
			}
		}
	} else if len(arguments) == 2 {
		name, nameOK := arguments[0].(string)
		value, valueOK := arguments[1].(string)
		if nameOK && valueOK {
			updates = append(updates, requestMIMEType{name: name, values: splitMIMEValues(value)})
		}
	}
	state.mu.Lock()
	for _, update := range updates {
		state.mimeTypes = mergeRequestMIMEType(state.mimeTypes, update)
	}
	state.mu.Unlock()
	return r
}

// IsCli 判断请求是否处于无 HTTP 原生请求的命令行上下文。
func (r *Request) IsCli() bool {
	return r == nil || r.raw == nil
}

// IsCgi 判断请求是否由 CGI 网关注入。
func (r *Request) IsCgi() bool {
	return strings.HasPrefix(strings.ToLower(r.serverValue("GATEWAY_INTERFACE")), "cgi")
}

// IsValidIP 检查 IPv4、IPv6 或任意合法 IP。
func (r *Request) IsValidIP(ip string, types ...string) bool {
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil {
		return false
	}
	typeName := ""
	if len(types) > 0 {
		typeName = strings.ToLower(strings.TrimSpace(types[0]))
	}
	switch typeName {
	case "ipv4":
		return parsed.To4() != nil
	case "ipv6":
		return parsed.To4() == nil && parsed.To16() != nil
	default:
		return true
	}
}

// Ip2bin 将合法 IP 转换为与 ThinkPHP 相同长度的二进制字符串。
func (r *Request) Ip2bin(ip string) string {
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil {
		return ""
	}
	bytes := parsed.To4()
	if bytes == nil {
		bytes = parsed.To16()
	}
	var result strings.Builder
	result.Grow(len(bytes) * 8)
	for _, value := range bytes {
		_, _ = fmt.Fprintf(&result, "%08b", value)
	}
	return result.String()
}

// IsMobile 按 ThinkPHP 的代理头、WAP Accept 和移动端 User-Agent 规则判断。
func (r *Request) IsMobile() bool {
	if strings.Contains(strings.ToLower(r.headerValue("Via")), "wap") ||
		strings.Contains(strings.ToUpper(r.headerValue("Accept")), "VND.WAP.WML") ||
		r.headerValue("X-Wap-Profile") != "" || r.headerValue("Profile") != "" {
		return true
	}
	userAgent := strings.ToLower(r.headerValue("User-Agent"))
	for _, marker := range requestMobileUserAgentMarkers {
		if strings.Contains(userAgent, marker) {
			return true
		}
	}
	return false
}

// SecureKey 返回请求内稳定、请求间隔离的随机安全键。
func (r *Request) SecureKey() string {
	state := r.thinkPHPState()
	if state == nil {
		return ""
	}
	state.mu.RLock()
	key := state.secureKey
	state.mu.RUnlock()
	if key != "" {
		return key
	}
	random := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, random); err != nil {
		return ""
	}
	generated := hex.EncodeToString(random)
	state.mu.Lock()
	if state.secureKey == "" {
		state.secureKey = generated
	}
	key = state.secureKey
	state.mu.Unlock()
	return key
}

var requestMobileUserAgentMarkers = []string{
	"blackberry", "configuration/cldc", "hp ", "hp-", "htc ", "htc_", "htc-", "iemobile",
	"kindle", "midp", "mmp", "motorola", "mobile", "nokia", "opera mini", "opera ",
	"googlebot-mobile", "yahooseeker/m1a1-r2d2", "android", "iphone", "ipod", "mobi", "palm",
	"palmos", "pocket", "portalmmm", "ppc;", "smartphone", "sonyericsson", "sqh", "spv",
	"symbian", "treo", "up.browser", "up.link", "vodafone", "windows ce", "xda ", "xda_",
}

func (r *Request) resolveBaseFile() string {
	if r.IsCli() {
		return ""
	}
	scriptFilename := strings.ReplaceAll(r.serverValue("SCRIPT_FILENAME"), "\\", "/")
	scriptBase := filepath.Base(scriptFilename)
	if scriptBase == "" || scriptBase == "." {
		return ""
	}
	for _, key := range []string{"SCRIPT_NAME", "PHP_SELF", "ORIG_SCRIPT_NAME"} {
		candidate := strings.ReplaceAll(r.serverValue(key), "\\", "/")
		if urlpath.Base(candidate) == scriptBase {
			return candidate
		}
	}
	phpSelf := strings.ReplaceAll(r.serverValue("PHP_SELF"), "\\", "/")
	if index := strings.Index(phpSelf, "/"+scriptBase); index >= 0 {
		scriptName := strings.ReplaceAll(r.serverValue("SCRIPT_NAME"), "\\", "/")
		if index <= len(scriptName) {
			return scriptName[:index] + "/" + scriptBase
		}
	}
	documentRoot := strings.TrimRight(strings.ReplaceAll(r.serverValue("DOCUMENT_ROOT"), "\\", "/"), "/")
	if documentRoot != "" && strings.HasPrefix(strings.ToLower(scriptFilename), strings.ToLower(documentRoot)+"/") {
		return "/" + strings.TrimLeft(scriptFilename[len(documentRoot):], "/")
	}
	return ""
}

func (r *Request) completeRequestPath(value string, complete []bool) string {
	if len(complete) == 0 || !complete[0] {
		return value
	}
	return r.Domain() + ensureRequestPathPrefix(value)
}

func splitMIMEValues(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func mergeRequestMIMEType(types []requestMIMEType, update requestMIMEType) []requestMIMEType {
	update.name = strings.TrimSpace(update.name)
	if update.name == "" {
		return types
	}
	if types == nil {
		// 后续修改只替换条目的值切片，外层复制即可隔离默认定义与其他请求。
		types = append([]requestMIMEType(nil), defaultRequestMIMETypes[:]...)
	}
	for index := range types {
		if types[index].name == update.name {
			types[index].values = append([]string(nil), update.values...)
			return types
		}
	}
	return append(types, requestMIMEType{name: update.name, values: append([]string(nil), update.values...)})
}
