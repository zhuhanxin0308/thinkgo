package context

import (
	"mime"
	"net"
	"net/url"
	"path"
	"strconv"
	"strings"
)

// SetLayer 设置当前控制器分层名。
func (r *Request) SetLayer(layer string) *Request {
	if r == nil {
		return r
	}
	r.metadataMu.Lock()
	r.layerValue = layer
	r.metadataMu.Unlock()
	return r
}

// SetController 设置当前控制器名。
func (r *Request) SetController(controller string) *Request {
	if r == nil {
		return r
	}
	r.metadataMu.Lock()
	r.controllerValue = controller
	r.metadataMu.Unlock()
	return r
}

// SetAction 设置当前控制器动作名。
func (r *Request) SetAction(action string) *Request {
	if r == nil {
		return r
	}
	r.metadataMu.Lock()
	r.actionValue = action
	r.metadataMu.Unlock()
	return r
}

// Layer 获取当前控制器分层名；convert=true 时转换为小写。
func (r *Request) Layer(convert ...bool) string {
	if r == nil {
		return ""
	}
	r.metadataMu.RLock()
	value := r.layerValue
	r.metadataMu.RUnlock()
	if len(convert) > 0 && convert[0] {
		return strings.ToLower(value)
	}
	return value
}

// Controller 获取当前控制器名；第一个选项控制小写转换，第二个选项仅返回 basename。
func (r *Request) Controller(options ...bool) string {
	if r == nil {
		return ""
	}
	r.metadataMu.RLock()
	value := r.controllerValue
	r.metadataMu.RUnlock()
	if len(options) > 1 && options[1] {
		if index := strings.LastIndexAny(value, "./"); index >= 0 {
			value = value[index+1:]
		}
	}
	if len(options) > 0 && options[0] {
		return strings.ToLower(value)
	}
	return value
}

// Action 获取当前控制器动作名；convert=true 时转换为小写。
func (r *Request) Action(convert ...bool) string {
	if r == nil {
		return ""
	}
	r.metadataMu.RLock()
	value := r.actionValue
	r.metadataMu.RUnlock()
	if len(convert) > 0 && convert[0] {
		return strings.ToLower(value)
	}
	return value
}

// IsSsl 判断原生 TLS 或受信代理链是否声明 HTTPS。
func (r *Request) IsSsl() bool {
	return r != nil && r.raw != nil && isHTTPS(r.raw, r.trustedProxies)
}

// Scheme 获取请求协议。
func (r *Request) Scheme() string {
	if r.IsSsl() {
		return "https"
	}
	return "http"
}

// Query 获取原始查询字符串。
func (r *Request) Query() string {
	if r == nil || r.raw == nil || r.raw.URL == nil {
		return ""
	}
	return r.raw.URL.RawQuery
}

// Port 获取当前请求的服务器端口。
func (r *Request) Port() int {
	host := r.requestHostValue()
	if host != "" && !isValidAbsoluteRequestHost(host) {
		return 0
	}
	if _, port, err := net.SplitHostPort(host); err == nil {
		parsed, parseErr := strconv.Atoi(port)
		if parseErr == nil {
			return parsed
		}
	}
	if r.IsSsl() {
		return defaultHTTPSPort
	}
	return defaultHTTPPort
}

// Protocol 获取原生 SERVER_PROTOCOL。
func (r *Request) Protocol() string {
	if r == nil || r.raw == nil {
		return ""
	}
	return r.raw.Proto
}

// RemotePort 获取客户端连接端口。
func (r *Request) RemotePort() int {
	if r == nil || r.raw == nil {
		return 0
	}
	_, port, err := net.SplitHostPort(strings.TrimSpace(r.raw.RemoteAddr))
	if err != nil {
		return 0
	}
	parsed, err := strconv.Atoi(port)
	if err != nil {
		return 0
	}
	return parsed
}

// Domain 获取包含协议的当前域名；removePort=true 时去除端口。
func (r *Request) Domain(removePort ...bool) string {
	if r == nil {
		return ""
	}
	r.metadataMu.RLock()
	domain, domainSet := r.domainValue, r.domainSet
	r.metadataMu.RUnlock()
	if domainSet && domain != "" {
		parsed, err := url.Parse(domain)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || !isValidAbsoluteRequestHost(parsed.Host) {
			return ""
		}
		if len(removePort) > 0 && removePort[0] {
			return parsed.Scheme + "://" + parsed.Hostname()
		}
		return parsed.Scheme + "://" + parsed.Host
	}
	host := r.absoluteHost()
	if host == "" {
		return ""
	}
	if len(removePort) > 0 && removePort[0] {
		host = requestHostname(host)
	}
	return r.Scheme() + "://" + host
}

// SetDomain 设置包含协议的当前域名。
func (r *Request) SetDomain(domain string) *Request {
	if r == nil {
		return r
	}
	r.metadataMu.Lock()
	r.domainValue = strings.TrimSpace(domain)
	r.domainSet = true
	r.metadataMu.Unlock()
	return r
}

// Url 返回请求目标；complete=true 时返回带协议和主机的绝对地址。
func (r *Request) Url(complete ...bool) string {
	if r == nil {
		return ""
	}
	r.metadataMu.RLock()
	value, configured := r.urlValue, r.urlSet
	r.metadataMu.RUnlock()
	if !configured {
		if r.raw == nil || r.raw.URL == nil {
			return ""
		}
		value = r.raw.URL.RequestURI()
	}
	if len(complete) == 0 || !complete[0] {
		return value
	}
	domain := r.Domain()
	if domain == "" {
		return ""
	}
	return domain + ensureRequestPathPrefix(value)
}

// SetUrl 设置当前完整 URL。
func (r *Request) SetUrl(value string) *Request {
	if r == nil {
		return r
	}
	r.metadataMu.Lock()
	r.urlValue = value
	r.urlSet = true
	r.metadataMu.Unlock()
	return r
}

// BaseUrl 获取不带查询字符串的当前 URL；complete=true 时包含域名。
func (r *Request) BaseUrl(complete ...bool) string {
	if r == nil {
		return ""
	}
	r.metadataMu.RLock()
	value, configured := r.baseURLValue, r.baseURLSet
	r.metadataMu.RUnlock()
	if !configured {
		value = r.Url()
		if index := strings.IndexByte(value, '?'); index >= 0 {
			value = value[:index]
		}
	}
	if len(complete) == 0 || !complete[0] {
		return value
	}
	domain := r.Domain()
	if domain == "" {
		return ""
	}
	return domain + ensureRequestPathPrefix(value)
}

// SetBaseUrl 设置不带查询字符串的当前 URL。
func (r *Request) SetBaseUrl(value string) *Request {
	if r == nil {
		return r
	}
	r.metadataMu.Lock()
	r.baseURLValue = value
	r.baseURLSet = true
	r.metadataMu.Unlock()
	return r
}

// Root 获取 URL 访问根地址；Go HTTP 宿主没有 PHP 脚本文件，默认根路径为空。
func (r *Request) Root(complete ...bool) string {
	if r == nil {
		return ""
	}
	r.metadataMu.RLock()
	value, configured := r.rootValue, r.rootSet
	r.metadataMu.RUnlock()
	if !configured {
		value = r.BaseFile()
		if value != "" && !strings.HasPrefix(r.Url(), value) {
			value = path.Dir(strings.ReplaceAll(value, "\\", "/"))
			if value == "." {
				value = ""
			}
		}
		value = strings.TrimRight(value, "/")
	}
	if len(complete) == 0 || !complete[0] {
		return value
	}
	domain := r.Domain()
	if domain == "" {
		return ""
	}
	return domain + ensureRequestPathPrefix(value)
}

// SetRoot 设置 URL 访问根地址。
func (r *Request) SetRoot(value string) *Request {
	if r == nil {
		return r
	}
	r.metadataMu.Lock()
	r.rootValue = strings.TrimRight(value, "/")
	r.rootSet = true
	r.metadataMu.Unlock()
	return r
}

// ContentType 获取去除参数后的请求内容类型，缺失时返回空字符串。
func (r *Request) ContentType() string {
	value := strings.TrimSpace(r.headerValue("Content-Type"))
	if value == "" {
		return ""
	}
	mediaType, _, err := mime.ParseMediaType(value)
	if err == nil {
		return strings.TrimSpace(mediaType)
	}
	if mediaType, _, found := strings.Cut(value, ";"); found {
		return strings.TrimSpace(mediaType)
	}
	return value
}

// MediaType 返回去除参数并规范为小写的媒体类型。
func (r *Request) MediaType() (string, error) {
	return r.parsedMediaType()
}

// Server 按 ThinkPHP 的常用 $_SERVER 键名映射 net/http 请求元数据。
func (r *Request) Server(key string) string {
	return r.applyRequestFilters(r.serverValue(key))
}

func (r *Request) serverValue(key string) string {
	key = strings.ToUpper(strings.TrimSpace(key))
	if overridden, configured := r.serverOverride(key); configured {
		value, valid := stringifyRequestValue(overridden)
		if valid {
			return value
		}
		return ""
	}
	if r == nil || r.raw == nil {
		return ""
	}
	switch key {
	case "REQUEST_METHOD":
		return r.Method(true)
	case "REQUEST_URI":
		if r.raw.URL != nil {
			return r.raw.URL.RequestURI()
		}
	case "QUERY_STRING":
		return r.Query()
	case "PATH_INFO":
		return r.Path()
	case "HTTP_HOST":
		return r.requestHostValue()
	case "SERVER_NAME":
		return r.Host(true)
	case "SERVER_PORT":
		if port := r.Port(); port > 0 {
			return strconv.Itoa(port)
		}
	case "SERVER_PROTOCOL":
		return r.Protocol()
	case "REMOTE_ADDR":
		host, _, err := net.SplitHostPort(strings.TrimSpace(r.raw.RemoteAddr))
		if err == nil {
			return host
		}
		return strings.TrimSpace(r.raw.RemoteAddr)
	case "REMOTE_PORT":
		if port := r.RemotePort(); port > 0 {
			return strconv.Itoa(port)
		}
	case "REQUEST_SCHEME":
		return r.Scheme()
	case "HTTPS":
		if r.IsSsl() {
			return "on"
		}
	case "CONTENT_TYPE", "HTTP_CONTENT_TYPE":
		return r.headerValue("Content-Type")
	case "CONTENT_LENGTH", "HTTP_CONTENT_LENGTH":
		if r.raw.ContentLength >= 0 {
			return strconv.FormatInt(r.raw.ContentLength, 10)
		}
	}
	key = strings.TrimPrefix(key, "HTTP_")
	return r.headerValue(strings.ReplaceAll(key, "_", "-"))
}
