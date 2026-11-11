package exception

import (
	"mime"
	"net/http"
	"strings"
)

type exceptionMedia uint8

const (
	exceptionMediaHTML exceptionMedia = iota
	exceptionMediaJSON
	exceptionMediaPlain
)

// exceptionResponseMedia 保留完整媒体选择，避免把纯文本误当成本机 HTML 调试页。
func exceptionResponseMedia(r *http.Request) exceptionMedia {
	if r == nil {
		return exceptionMediaHTML
	}
	accept := strings.TrimSpace(r.Header.Get("Accept"))
	if accept != "" {
		jsonPreference, htmlPreference, plainPreference, recognized := negotiateExceptionMedia(accept)
		if !recognized {
			return exceptionMediaHTML
		}
		if preferExceptionMedia(plainPreference, jsonPreference) && preferExceptionMedia(plainPreference, htmlPreference) {
			return exceptionMediaPlain
		}
		// 只有可接受的全通配范围不表达格式偏好；明确类型、权重和拒绝仍优先。
		wildcardOnly := jsonPreference.set && htmlPreference.set &&
			jsonPreference.specificity == 0 && htmlPreference.specificity == 0 &&
			jsonPreference.quality > 0 && jsonPreference.quality == htmlPreference.quality
		if !wildcardOnly {
			if preferExceptionMedia(jsonPreference, htmlPreference) {
				return exceptionMediaJSON
			}
			return exceptionMediaHTML
		}
	}
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Requested-With")), "XMLHttpRequest") {
		return exceptionMediaJSON
	}
	if r.URL != nil && (r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/")) {
		return exceptionMediaJSON
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err == nil && isJSONMediaType(mediaType) {
		return exceptionMediaJSON
	}
	return exceptionMediaHTML
}
