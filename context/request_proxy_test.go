package context

import (
	"bytes"
	"errors"
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

	wrapped := newRequestForTest(t, req)

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

	wrapped := newRequestForTest(t, req, WithTrustedProxies([]string{"127.0.0.1/32"}))

	if wrapped.Ip() != "203.0.113.8" {
		t.Fatalf("来自受信代理时应解析真实客户端 IP，实际为 %q", wrapped.Ip())
	}
	if !wrapped.IsSsl() {
		t.Fatal("来自受信代理时应信任 X-Forwarded-Proto=https")
	}
}

// TestRequestIgnoresSpoofedLeftMostForwardedFor 验证受信代理链会从右向左剥离受信节点，
// 避免客户端预先伪造的 X-Forwarded-For 首段被误认为真实来源。
func TestRequestIgnoresSpoofedLeftMostForwardedFor(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://example.com/profile", nil)
	req.RemoteAddr = "10.0.0.10:4321"
	req.Header.Set("X-Forwarded-For", "198.51.100.250, 203.0.113.8, 10.0.0.5")

	wrapped := newRequestForTest(t, req, WithTrustedProxies([]string{"10.0.0.0/24"}))

	if wrapped.Ip() != "203.0.113.8" {
		t.Fatalf("应返回离受信代理最近的非受信客户端 IP，实际为 %q", wrapped.Ip())
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

	wrapped := newRequestForTest(t, req, WithMultipartMemoryLimit(1))
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

	if err = wrapped.Cleanup(); err != nil {
		t.Fatalf("清理 multipart 临时文件失败: %v", err)
	}

	entries, err = os.ReadDir(tempDir)
	if err != nil {
		t.Fatalf("读取清理后的临时目录失败: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("请求清理后不应残留 multipart 临时文件，实际残留 %d 个", len(entries))
	}
}

// TestNewRequestRejectsInvalidOptions 验证安全相关请求选项必须显式校验，禁止静默忽略错误配置。
func TestNewRequestRejectsInvalidOptions(t *testing.T) {
	tests := []struct {
		name   string
		option RequestOption
		target error
	}{
		{name: "非法代理网段", option: WithTrustedProxies([]string{"127.0.0.1/33"}), target: ErrInvalidTrustedProxy},
		{name: "空代理项", option: WithTrustedProxies([]string{"127.0.0.1", " "}), target: ErrInvalidTrustedProxy},
		{name: "非正数 multipart 内存上限", option: WithMultipartMemoryLimit(0), target: ErrInvalidMultipartMemoryLimit},
		{name: "过大 multipart 内存上限", option: WithMultipartMemoryLimit((1 << 30) + 1), target: ErrInvalidMultipartMemoryLimit},
		{name: "非正数请求体上限", option: WithMaxBodyBytes(-1), target: ErrInvalidMaxBodyBytes},
		{name: "过大请求体上限", option: WithMaxBodyBytes((1 << 30) + 1), target: ErrInvalidMaxBodyBytes},
		{name: "空请求选项", option: nil, target: ErrInvalidRequestOption},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewRequest(httptest.NewRequest(http.MethodGet, "/", nil), test.option)
			if !errors.Is(err, test.target) {
				t.Fatalf("应返回 %v，实际为 %v", test.target, err)
			}
		})
	}
}

// TestRequestUsesRightMostForwardedProto 验证受信代理追加协议时不会被客户端预置的左侧伪造值覆盖。
func TestRequestUsesRightMostForwardedProto(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://example.com/profile", nil)
	req.RemoteAddr = "10.0.0.10:4321"
	req.Header.Set("X-Forwarded-Proto", "https, http")

	wrapped := newRequestForTest(t, req, WithTrustedProxies([]string{"10.0.0.0/24"}))
	if wrapped.IsSsl() {
		t.Fatal("协议判断必须采用受信链路最右侧值，不能接受客户端伪造的左侧 https")
	}
}

// TestMultipartBodyLimitPreservesTooLargeError 验证 multipart 包装错误仍可识别请求体超限并映射为 413。
func TestMultipartBodyLimitPreservesTooLargeError(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	field, err := writer.CreateFormField("payload")
	if err != nil {
		t.Fatalf("创建 multipart 字段失败: %v", err)
	}
	if _, err = field.Write(bytes.Repeat([]byte("x"), 256)); err != nil {
		t.Fatalf("写入 multipart 字段失败: %v", err)
	}
	if err = writer.Close(); err != nil {
		t.Fatalf("关闭 multipart writer 失败: %v", err)
	}

	raw := httptest.NewRequest(http.MethodPost, "http://example.com/upload", &body)
	raw.ContentLength = -1
	raw.Header.Set("Content-Type", writer.FormDataContentType())
	req := newRequestForTest(t, raw, WithMaxBodyBytes(64))
	if err = req.Parse(); !errors.Is(err, ErrRequestBodyTooLarge) {
		t.Fatalf("multipart 超限必须保留 ErrRequestBodyTooLarge，实际为 %v", err)
	}
	if cleanupErr := req.Cleanup(); cleanupErr != nil {
		t.Fatalf("清理超限 multipart 请求失败: %v", cleanupErr)
	}
}

// TestRequestOptionsUseFinalBodyLimit 验证构造期重复设置上限时由最后一个选项生效。
func TestRequestOptionsUseFinalBodyLimit(t *testing.T) {
	raw := httptest.NewRequest(http.MethodPost, "http://example.com/form", bytes.NewReader(bytes.Repeat([]byte("x"), 8)))
	request := newRequestForTest(t, raw, WithMaxBodyBytes(4), WithMaxBodyBytes(16))
	if err := request.Parse(); err != nil {
		t.Fatalf("最终请求体上限应允许 8 字节正文: %v", err)
	}
}

func newRequestForTest(t *testing.T, raw *http.Request, options ...RequestOption) *Request {
	t.Helper()
	req, err := NewRequest(raw, options...)
	if err != nil {
		t.Fatalf("创建请求失败: %v", err)
	}
	return req
}
