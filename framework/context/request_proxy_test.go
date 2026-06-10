package context

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// TestRequestIgnoresProxyHeadersWithoutTrustedProxy 验证默认情况下不会盲目信任客户端自带的代理头。
func TestRequestIgnoresProxyHeadersWithoutTrustedProxy(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://example.com/profile", nil)
	req.RemoteAddr = "198.51.100.10:4321"
	req.Header.Set("X-Forwarded-For", "203.0.113.8")
	req.Header.Set("X-Real-IP", "203.0.113.9")
	req.Header.Set("X-Forwarded-Proto", "https")

	wrapped := NewRequest(req)

	if wrapped.Ip() != "198.51.100.10" {
		t.Fatalf("默认情况下应回退到真实连接地址，实际为 %q", wrapped.Ip())
	}
	if wrapped.IsSsl() {
		t.Fatal("默认情况下不应仅凭客户端伪造的 X-Forwarded-Proto 判定为 HTTPS")
	}
}

// TestRequestUsesProxyHeadersFromTrustedProxy 验证仅当请求来自受信代理时才解析代理头。
func TestRequestUsesProxyHeadersFromTrustedProxy(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://example.com/profile", nil)
	req.RemoteAddr = "127.0.0.1:4321"
	req.Header.Set("X-Forwarded-For", "203.0.113.8, 127.0.0.1")
	req.Header.Set("X-Real-IP", "203.0.113.9")
	req.Header.Set("X-Forwarded-Proto", "https")

	wrapped := NewRequest(req, WithTrustedProxies([]string{"127.0.0.1/32"}))

	if wrapped.Ip() != "203.0.113.8" {
		t.Fatalf("来自受信代理时应解析真实客户端 IP，实际为 %q", wrapped.Ip())
	}
	if !wrapped.IsSsl() {
		t.Fatal("来自受信代理时应信任 X-Forwarded-Proto=https")
	}
}

// TestRequestCleanupRemovesMultipartTempFiles 验证 multipart 解析产生的临时文件会在请求结束后被清理。
func TestRequestCleanupRemovesMultipartTempFiles(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("TMPDIR", tempDir)
	t.Setenv("TMP", tempDir)
	t.Setenv("TEMP", tempDir)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	fileWriter, err := writer.CreateFormFile("file", "large.txt")
	if err != nil {
		t.Fatalf("创建 multipart 文件字段失败: %v", err)
	}
	if _, err = io.Copy(fileWriter, bytes.NewReader(bytes.Repeat([]byte("a"), 4096))); err != nil {
		t.Fatalf("写入 multipart 内容失败: %v", err)
	}
	if err = writer.Close(); err != nil {
		t.Fatalf("关闭 multipart writer 失败: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "http://example.com/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	wrapped := NewRequest(req, WithMultipartMemoryLimit(1))
	if _, err = wrapped.File("file"); err != nil {
		t.Fatalf("解析上传文件失败: %v", err)
	}

	entries, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatalf("读取临时目录失败: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("测试前提不成立：应先生成 multipart 临时文件")
	}

	wrapped.Cleanup()

	entries, err = os.ReadDir(tempDir)
	if err != nil {
		t.Fatalf("读取清理后的临时目录失败: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("请求清理后不应残留 multipart 临时文件，实际残留 %d 个", len(entries))
	}
}
