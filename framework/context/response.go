package context

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
)

var jsonpCallbackPattern = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$\.\[\]]*$`)

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
	r.header.Set(key, value)
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
		r.body = []byte(err.Error())
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
		return r.Abort(http.StatusInternalServerError, map[string]interface{}{
			"message": err.Error(),
		})
	}

	r.streamWriter = nil
	r.chunks = nil
	r.filePath = ""
	r.Header("Content-Type", "application/javascript; charset=utf-8")
	r.body = []byte(fmt.Sprintf("%s(%s);", callback, payload))
	return r
}

// Redirect 设置重定向响应。
func (r *Response) Redirect(target string, code ...int) *Response {
	status := http.StatusFound
	if len(code) > 0 {
		status = code[0]
	}
	r.Header("Location", target)
	r.Code(status)
	return r
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
			_, _ = w.Write([]byte(err.Error()))
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
func (r *Response) Download(filepath, filename string) *Response {
	r.Header("Content-Description", "File Transfer")
	r.Header("Content-Type", "application/octet-stream")
	r.Header(
		"Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, filename, url.PathEscape(filename)),
	)
	r.Header("Content-Transfer-Encoding", "binary")
	r.Header("Expires", "0")
	r.Header("Cache-Control", "must-revalidate")
	r.Header("Pragma", "public")
	r.filePath = filepath
	r.streamWriter = nil
	r.chunks = nil
	r.body = nil
	return r
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
		r.body = []byte(err.Error())
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
