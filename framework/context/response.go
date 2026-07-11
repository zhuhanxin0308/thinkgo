package context

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

var jsonpCallbackPattern = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$\.\[\]]*$`)

const internalServerErrorMessage = "internal server error"

// Response 封装框架响应对象，负责统一管理状态码、头部和输出内容。
type Response struct {
	status       int
	header       http.Header
	body         []byte
	filePath     string
	streamWriter func(io.Writer) error
	chunks       [][]byte
}

// GetBody 获取响应体字节。
func (r *Response) GetBody() []byte {
	return r.body
}

// GetStatus 获取 HTTP 状态码。
func (r *Response) GetStatus() int {
	return r.status
}

// NewResponse 创建响应实例。
func NewResponse() *Response {
	return &Response{
		status: http.StatusOK,
		header: make(http.Header),
	}
}

// Header 设置响应头。
func (r *Response) Header(key, value string) *Response {
	key = strings.TrimSpace(key)
	if !isValidHeaderName(key) {
		return r
	}
	r.header.Set(key, sanitizeHeaderValue(value))
	return r
}

// Headers 获取全部响应头。
func (r *Response) Headers() http.Header {
	return r.header
}

// Code 设置状态码。
func (r *Response) Code(code int) *Response {
	r.status = code
	return r
}

// Content 设置文本内容。
func (r *Response) Content(content string) *Response {
	r.streamWriter = nil
	r.chunks = nil
	r.filePath = ""
	r.body = []byte(content)
	return r
}

// Json 设置 JSON 响应。
func (r *Response) Json(data interface{}) *Response {
	r.streamWriter = nil
	r.chunks = nil
	r.filePath = ""
	r.Header("Content-Type", "application/json")
	bytes, err := json.Marshal(data)
	if err != nil {
		r.status = http.StatusInternalServerError
		r.body = []byte(`{"message":"` + internalServerErrorMessage + `"}`)
		return r
	}
	r.body = bytes
	return r
}

// Jsonp 设置 JSONP 响应，并严格校验回调名，避免脚本注入。
func (r *Response) Jsonp(callback string, data interface{}) *Response {
	callback = regexp.MustCompile(`\s+`).ReplaceAllString(callback, "")
	if !jsonpCallbackPattern.MatchString(callback) {
		return r.Abort(http.StatusBadRequest, map[string]interface{}{
			"message": "invalid jsonp callback",
		})
	}

	payload, err := json.Marshal(data)
	if err != nil {
		r.streamWriter = nil
		r.chunks = nil
		r.filePath = ""
		r.Header("Content-Type", "application/javascript; charset=utf-8")
		r.status = http.StatusInternalServerError
		r.body = []byte(fmt.Sprintf("%s({\"message\":\"%s\"});", callback, internalServerErrorMessage))
		return r
	}

	r.streamWriter = nil
	r.chunks = nil
	r.filePath = ""
	r.Header("Content-Type", "application/javascript; charset=utf-8")
	r.body = []byte(fmt.Sprintf("%s(%s);", callback, payload))
	return r
}

// Redirect 设置重定向响应。
// target 中的 CR/LF 等控制字符会被剥离，防止 Location 头注入与 HTTP 响应拆分。
// 注意：本方法不限制跳转目标的来源，若 target 来自用户输入，调用方需自行防范开放重定向。
func (r *Response) Redirect(target string, code ...int) *Response {
	status := http.StatusFound
	if len(code) > 0 {
		status = code[0]
	}
	r.Header("Location", sanitizeHeaderValue(target))
	r.Code(status)
	return r
}

// sanitizeHeaderValue 移除可能导致响应头注入/拆分的控制字符（CR、LF、NUL）。
func sanitizeHeaderValue(value string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == 0 {
			return -1
		}
		return r
	}, value)
}

// isValidHeaderName 按 HTTP token 规则校验响应头名，非法头名直接拒绝写入。
func isValidHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for index := 0; index < len(name); index++ {
		if !isHeaderTokenChar(name[index]) {
			return false
		}
	}
	return true
}

// isHeaderTokenChar 判断字符是否可用于 HTTP 头字段名。
func isHeaderTokenChar(char byte) bool {
	if char >= 'a' && char <= 'z' {
		return true
	}
	if char >= 'A' && char <= 'Z' {
		return true
	}
	if char >= '0' && char <= '9' {
		return true
	}
	switch char {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	default:
		return false
	}
}

// Stream 设置流式响应回调。
func (r *Response) Stream(writer func(io.Writer) error) *Response {
	r.streamWriter = writer
	r.chunks = nil
	r.filePath = ""
	r.body = nil
	if r.header.Get("Content-Type") == "" {
		r.Header("Content-Type", "application/octet-stream")
	}
	return r
}

// Chunk 设置分块响应数据。
func (r *Response) Chunk(chunks [][]byte) *Response {
	r.streamWriter = nil
	r.filePath = ""
	r.body = nil
	r.chunks = make([][]byte, 0, len(chunks))
	for _, chunk := range chunks {
		if len(chunk) == 0 {
			continue
		}
		copied := append([]byte(nil), chunk...)
		r.chunks = append(r.chunks, copied)
	}
	if len(r.chunks) > 0 {
		r.Header("Transfer-Encoding", "chunked")
	}
	return r
}

// NoContent 设置 204 空响应。
func (r *Response) NoContent() *Response {
	r.status = http.StatusNoContent
	r.streamWriter = nil
	r.chunks = nil
	r.filePath = ""
	r.body = nil
	return r
}

// Abort 立即生成指定状态码的响应，支持字符串和结构化 JSON。
func (r *Response) Abort(code int, payload interface{}) *Response {
	r.Code(code)
	switch typed := payload.(type) {
	case nil:
		r.body = nil
	case string:
		r.Content(typed)
	default:
		r.Json(typed)
		r.Code(code)
	}
	return r
}

// Send 输出响应到客户端。
func (r *Response) Send(w http.ResponseWriter) {
	for key, values := range r.header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	if r.status == http.StatusNoContent {
		w.WriteHeader(r.status)
		return
	}

	if r.filePath != "" {
		file, err := os.Open(r.filePath)
		if err != nil {
			http.Error(w, "File not found", http.StatusNotFound)
			return
		}
		defer file.Close()

		w.WriteHeader(r.status)
		_, _ = io.Copy(w, file)
		return
	}

	if r.streamWriter != nil {
		w.WriteHeader(r.status)
		if err := r.streamWriter(w); err != nil {
			// 流式响应状态码已经写出，只能输出通用错误，避免泄露内部异常详情。
			_, _ = w.Write([]byte(internalServerErrorMessage))
		}
		return
	}

	if len(r.chunks) > 0 {
		w.WriteHeader(r.status)
		for _, chunk := range r.chunks {
			_, _ = w.Write(chunk)
		}
		return
	}

	w.WriteHeader(r.status)
	if len(r.body) > 0 {
		_, _ = w.Write(r.body)
	}
}

// Cookie 设置 Cookie。
func (r *Response) Cookie(name, value string, maxAge int, path, domain string, secure, httpOnly bool) *Response {
	cookie := &http.Cookie{
		Name:     name,
		Value:    value,
		MaxAge:   maxAge,
		Path:     path,
		Domain:   domain,
		Secure:   secure,
		HttpOnly: httpOnly,
	}
	r.header.Add("Set-Cookie", cookie.String())
	return r
}

// Download 设置文件下载响应。
//
// filePath 由调用方负责，必须是受信任的路径：若该路径可能来自用户输入，
// 请改用 DownloadSafe 把路径限制在允许的根目录内，避免目录穿越读取任意文件。
// filename 会被净化（取末段文件名并去除引号与换行），防止 Content-Disposition 头注入。
func (r *Response) Download(filePath, filename string) *Response {
	safeName := sanitizeDownloadFilename(filename)
	r.Header("Content-Description", "File Transfer")
	r.Header("Content-Type", "application/octet-stream")
	r.Header(
		"Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, safeName, url.PathEscape(safeName)),
	)
	r.Header("Content-Transfer-Encoding", "binary")
	r.Header("Expires", "0")
	r.Header("Cache-Control", "must-revalidate")
	r.Header("Pragma", "public")
	r.filePath = filePath
	r.streamWriter = nil
	r.chunks = nil
	r.body = nil
	return r
}

// DownloadSafe 在限定根目录内安全地提供文件下载。
// baseDir 是允许下载的根目录；relativePath 可能来自用户输入，框架会清理并阻断
// ".." 与绝对路径逃逸，确保最终文件落在 baseDir 内。解析越界时返回 403。
// filename 为空时使用解析后的文件名。
func (r *Response) DownloadSafe(baseDir, relativePath, filename string) *Response {
	target, ok := resolveWithinBase(baseDir, relativePath)
	if !ok {
		return r.Abort(http.StatusForbidden, map[string]interface{}{
			"message": "invalid download path",
		})
	}
	if strings.TrimSpace(filename) == "" {
		filename = filepath.Base(target)
	}
	return r.Download(target, filename)
}

// sanitizeDownloadFilename 取路径末段作为下载文件名，并去除可能破坏
// Content-Disposition 头的引号与换行，防止头注入/截断。
func sanitizeDownloadFilename(name string) string {
	if idx := strings.LastIndexAny(name, `/\`); idx >= 0 {
		name = name[idx+1:]
	}
	name = strings.NewReplacer("\r", "", "\n", "", `"`, "").Replace(name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return "download"
	}
	return name
}

// resolveWithinBase 把可能来自用户输入的相对路径解析到 baseDir 内。
// 返回 (绝对路径, 是否合法)。绝对路径、清理后仍试图越出根目录（".." 逃逸）
// 或最终落在 baseDir 之外的路径一律拒绝（返回 false），而非静默截断。
func resolveWithinBase(baseDir, relativePath string) (string, bool) {
	normalized := strings.ReplaceAll(relativePath, "\\", "/")

	// 绝对路径直接拒绝。
	if path.IsAbs(normalized) || filepath.IsAbs(relativePath) {
		return "", false
	}

	cleaned := path.Clean(normalized)
	// 清理后为空目录、上级目录或仍以 ".." 开头，均视为非法。
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", false
	}

	target := filepath.Join(baseDir, filepath.FromSlash(cleaned))

	baseAbs, err := filepath.Abs(baseDir)
	if err != nil {
		return "", false
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return "", false
	}
	// 双重保险：解析后的绝对路径必须仍在 baseDir 内。
	if targetAbs != baseAbs && !strings.HasPrefix(targetAbs, baseAbs+string(os.PathSeparator)) {
		return "", false
	}
	return targetAbs, true
}

// Xml 设置 XML 响应。
func (r *Response) Xml(data interface{}) *Response {
	r.streamWriter = nil
	r.chunks = nil
	r.filePath = ""
	r.Header("Content-Type", "application/xml")
	bytes, err := xml.Marshal(data)
	if err != nil {
		r.status = http.StatusInternalServerError
		r.body = []byte(`<error><message>` + internalServerErrorMessage + `</message></error>`)
		return r
	}
	r.body = bytes
	return r
}

// ContentType 设置 Content-Type。
func (r *Response) ContentType(contentType string, charset ...string) *Response {
	if len(charset) > 0 {
		contentType = contentType + "; charset=" + charset[0]
	}
	r.Header("Content-Type", contentType)
	return r
}

// Expires 设置过期时间。
func (r *Response) Expires(time string) *Response {
	r.Header("Expires", time)
	return r
}

// LastModified 设置最后修改时间。
func (r *Response) LastModified(time string) *Response {
	r.Header("Last-Modified", time)
	return r
}

// CacheControl 设置缓存控制。
func (r *Response) CacheControl(cache string) *Response {
	r.Header("Cache-Control", cache)
	return r
}

// ETag 设置 ETag。
func (r *Response) ETag(eTag string) *Response {
	r.Header("ETag", eTag)
	return r
}
