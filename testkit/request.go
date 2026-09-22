package testkit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
)

const testOrigin = "http://example.com"

// Do 保留调用方请求的上下文、请求头与 Cookie，在内存中记录真实内核响应。
func (host *Host) Do(request *http.Request) (*httptest.ResponseRecorder, error) {
	if host == nil || host.handler == nil || request == nil {
		return nil, errors.New("测试宿主或请求为空")
	}
	if err := request.Context().Err(); err != nil {
		return nil, err
	}
	response := httptest.NewRecorder()
	host.ServeHTTP(response, request)
	return response, nil
}

// JSON 编码请求数据并发送 JSON 请求；需要自定义上下文或请求头时使用 Do。
// body 为 nil 时不发送请求体，可用于无请求体的 GET 和 DELETE。
func (host *Host) JSON(method, target string, body any) (*httptest.ResponseRecorder, error) {
	var reader io.Reader
	if body != nil {
		content, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(content)
	}
	if strings.HasPrefix(target, "/") {
		target = testOrigin + target
	}
	request, err := http.NewRequest(method, target, reader)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return host.Do(request)
}

// DecodeJSON 校验媒体类型并解码单个 JSON 值，不消耗记录器原有响应体。
// 动态数字使用 json.Number，避免游标与大整数在测试断言前丢失精度。
func DecodeJSON[T any](response *httptest.ResponseRecorder) (T, error) {
	var value T
	if response == nil {
		return value, errors.New("测试响应为空")
	}
	media, _, err := mime.ParseMediaType(response.Header().Get("Content-Type"))
	if err != nil || media != "application/json" && !strings.HasSuffix(media, "+json") {
		return value, fmt.Errorf("测试响应不是 JSON: %q", response.Header().Get("Content-Type"))
	}
	decoder := json.NewDecoder(bytes.NewReader(response.Body.Bytes()))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		var zero T
		return zero, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		var zero T
		return zero, errors.New("测试响应包含多余 JSON 数据")
	}
	return value, nil
}
