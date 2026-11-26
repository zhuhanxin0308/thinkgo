package testkit

import (
	"errors"
	"io"
	"net/http/httptest"
	"testing"
)

// TestDecodeJSONEmptyResponseBody 保证合法的空正文记录器返回错误，而不是中断测试。
func TestDecodeJSONEmptyResponseBody(t *testing.T) {
	for _, discard := range []bool{false, true} {
		name := "空缓冲区"
		if discard {
			name = "禁用正文记录"
		}
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			response.Header().Set("Content-Type", "application/json")
			if discard {
				response.Body = nil
				if _, err := response.Write([]byte(`{"name":"Ada"}`)); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				value, err := DecodeJSON[map[string]string](response)
				if !errors.Is(err, io.EOF) || value != nil {
					t.Fatalf("空正文应返回 EOF 和零值: value=%#v err=%v", value, err)
				}
			}
			if discard && response.Body != nil {
				t.Fatal("解码不得创建或替换调用方的正文缓冲区")
			}
		})
	}
}

// TestDecodeJSONDiscardedNonJSONBody 保留媒体类型错误的优先级。
func TestDecodeJSONDiscardedNonJSONBody(t *testing.T) {
	response := httptest.NewRecorder()
	response.Body = nil
	response.Header().Set("Content-Type", "text/plain")
	if _, err := response.Write([]byte("plain")); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeJSON[string](response); err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("非 JSON 响应应返回媒体类型错误: %v", err)
	}
}
