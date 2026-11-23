package context

import (
	"errors"
	"io"
	"net/http"
	"net/url"
)

// boundedOriginalBody 在实际消费前确认当前流的上限。中间件可依约顺序替换 Raw().Body，
// multipart 也必须读到同一受限流；保留原有限制器能继续计算已消费的字节。
func (r *Request) boundedOriginalBody() io.ReadCloser {
	if r == nil || r.raw == nil {
		return nil
	}
	r.errorMu.Lock()
	defer r.errorMu.Unlock()
	source := r.raw.Body
	if source == nil || source == http.NoBody {
		return source
	}
	if limited, ok := source.(*bodyLimitReadCloser); ok && limited.limit <= r.maxBodyBytes {
		return limited
	}
	r.raw.Body = newBodyLimitReadCloser(source, r.maxBodyBytes)
	return r.raw.Body
}

// compatiblePostForm 保留兼容访问器对普通解析错误的既有行为。超限时整份表单失效，
// 即使标准库已暂存字段也不能再暴露；MultipartForm 的临时文件仍交给 Cleanup 回收。
func (r *Request) compatiblePostForm() url.Values {
	if r == nil || r.raw == nil {
		return nil
	}
	if err := r.ensureFormParsed(); err != nil {
		// Cleanup 后入口优先报告 ErrRequestCleaned，已拒绝的表单仍不能重新可见。
		if errors.Is(err, ErrRequestCleaned) && !r.formFinished.Load() {
			// 正在收尾的请求不能等待未合作的读取器，也不能观察仍在写入的表单。
			return nil
		}
		r.errorMu.RLock()
		formErr := r.formErr
		r.errorMu.RUnlock()
		if errors.Is(formErr, ErrRequestBodyTooLarge) {
			return nil
		}
	}
	return r.raw.PostForm
}

// parseBoundedMultipartForm 只检查解析器实际读入的字节。若终止分隔线与超限探测字节
// 被预读到同一缓冲区，标准库可能成功完成解析而不再向读取器索取延迟的错误。
func (r *Request) parseBoundedMultipartForm() error {
	source := r.boundedOriginalBody()
	parseErr := normalizeBodyReadError(r.raw.ParseMultipartForm(r.multipartMemoryLimit), r.maxBodyBytes)
	// 构造后重新配置上限会保留原有限制器；任一层记录的超限都不能被缓冲解析吞掉。
	for {
		limited, ok := source.(*bodyLimitReadCloser)
		if !ok {
			return parseErr
		}
		if limited.consumed.Load() > limited.limit && !errors.Is(parseErr, ErrRequestBodyTooLarge) {
			return errors.Join(normalizeBodyReadError(ErrRequestBodyTooLarge, r.maxBodyBytes), parseErr)
		}
		source = limited.source
	}
}

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
