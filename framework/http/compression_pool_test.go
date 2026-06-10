package http

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// TestCompressionWriterReusesPooledGzipWriter 验证同编码压缩器会回收到池中并被后续请求复用。
func TestCompressionWriterReusesPooledGzipWriter(t *testing.T) {
	levels := map[string]int{"gzip": 1}
	payload := strings.Repeat("payload-", 8)

	req1 := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
	req1.Header.Set("Accept-Encoding", "gzip")
	writer1 := NewCompressionResponseWriter(httptest.NewRecorder(), req1, 1, levels)
	if _, err := writer1.Write([]byte(payload)); err != nil {
		t.Fatalf("第一次压缩写入失败: %v", err)
	}
	if err := writer1.Close(); err != nil {
		t.Fatalf("第一次关闭压缩写入器失败: %v", err)
	}

	pooledField1 := reflect.ValueOf(writer1).Elem().FieldByName("pooledWriter")
	if !pooledField1.IsValid() || pooledField1.IsNil() {
		t.Fatal("CompressionResponseWriter 应持有当前借出的池化压缩器")
	}
	firstLeasePtr := pooledField1.Pointer()

	req2 := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
	req2.Header.Set("Accept-Encoding", "gzip")
	writer2 := NewCompressionResponseWriter(httptest.NewRecorder(), req2, 1, levels)
	if _, err := writer2.Write([]byte(payload)); err != nil {
		t.Fatalf("第二次压缩写入失败: %v", err)
	}
	if err := writer2.Close(); err != nil {
		t.Fatalf("第二次关闭压缩写入器失败: %v", err)
	}

	pooledField2 := reflect.ValueOf(writer2).Elem().FieldByName("pooledWriter")
	if !pooledField2.IsValid() || pooledField2.IsNil() {
		t.Fatal("第二次请求应同样拿到池化压缩器")
	}
	secondLeasePtr := pooledField2.Pointer()

	if firstLeasePtr != secondLeasePtr {
		t.Fatal("同编码压缩请求应复用同一个池化压缩器实例")
	}
}
