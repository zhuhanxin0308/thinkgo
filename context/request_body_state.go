package context

import (
	"io"
	"net/http"
)

// originalBody 只保护正文指针快照，不把阻塞读取或 Close 包在请求状态锁内。
func (r *Request) originalBody() io.ReadCloser {
	if r == nil || r.raw == nil {
		return nil
	}
	r.errorMu.RLock()
	body := r.raw.Body
	r.errorMu.RUnlock()
	return body
}

// replaceOriginalBody 与实体判断共用既有状态锁，覆盖成功恢复、失败关闭及表单恢复。
func (r *Request) replaceOriginalBody(body io.ReadCloser) {
	r.errorMu.Lock()
	r.raw.Body = body
	r.errorMu.Unlock()
}

// hasOriginalBody 保留零长度为空体的语义；空 GET 不访问会变化的正文指针，也不加锁。
func (r *Request) hasOriginalBody() bool {
	if r == nil || r.raw == nil || r.raw.ContentLength == 0 {
		return false
	}
	body := r.originalBody()
	return body != nil && body != http.NoBody
}
