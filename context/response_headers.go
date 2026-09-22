package context

import "net/http"

const defaultResponseContentType = "text/html; charset=utf-8"

// responseHeaders 单独保存最常见的单值 Content-Type，其余头部按需创建映射。
// 状态保持独立的共享对象，使 Response 值复制仍共享头部，而公开读取始终返回隔离快照。
type responseHeaders struct {
	values            http.Header
	contentType       string
	inlineContentType bool
}

// set 接受已经校验并规范化的头部名称，避免默认类型被 JSON 替换时创建无用映射和切片。
func (headers *responseHeaders) set(key, value string, appendValue bool) {
	if key == "Content-Type" {
		if headers.inlineContentType {
			if !appendValue {
				headers.contentType = value
				return
			}
			if headers.values == nil {
				headers.values = make(http.Header)
			}
			headers.values[key] = []string{headers.contentType, value}
			headers.contentType = ""
			headers.inlineContentType = false
			return
		}
		if len(headers.values[key]) == 0 {
			headers.contentType = value
			headers.inlineContentType = true
			return
		}
	}
	if headers.values == nil {
		headers.values = make(http.Header)
	}
	if appendValue {
		headers.values.Add(key, value)
		return
	}
	if values := headers.values[key]; len(values) > 0 {
		// 头部值仅由当前响应状态持有；公开读取均返回副本，可安全复用覆盖位置。
		values[0] = value
		clear(values[1:])
		headers.values[key] = values[:1]
	} else {
		headers.values[key] = []string{value}
	}
}

func (headers *responseHeaders) get(name string) string {
	if headers == nil {
		return ""
	}
	key := http.CanonicalHeaderKey(name)
	if key == "Content-Type" && headers.inlineContentType {
		return headers.contentType
	}
	if values := headers.values[key]; len(values) > 0 {
		return values[0]
	}
	return ""
}

// snapshot 为所有头值一次性分配隔离的存储，既保留空值和顺序，也避免按头重复分配切片。
func (headers *responseHeaders) snapshot() http.Header {
	if headers == nil {
		return nil
	}
	keyCount := len(headers.values)
	valueCount := 0
	for _, values := range headers.values {
		valueCount += len(values)
	}
	if headers.inlineContentType {
		keyCount++
		valueCount++
	}
	result := make(http.Header, keyCount)
	storage := make([]string, valueCount)
	if headers.inlineContentType {
		storage[0] = headers.contentType
		result["Content-Type"] = storage[:1:1]
		storage = storage[1:]
	}
	for key, values := range headers.values {
		if values == nil {
			result[key] = nil
			continue
		}
		copied := copy(storage, values)
		result[key] = storage[:copied:copied]
		storage = storage[copied:]
	}
	return result
}
