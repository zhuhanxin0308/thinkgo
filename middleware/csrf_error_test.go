package middleware

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	fwcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
)

type failingCSRFReader struct{ err error }

func (reader failingCSRFReader) Read([]byte) (int, error) { return 0, reader.err }

// TestCSRFDefaultConstructorAndSafeTokenRepair 验证默认随机密钥、篡改修复、重复 Cookie 与 nil 响应路径。
func TestCSRFDefaultConstructorAndSafeTokenRepair(t *testing.T) {
	handler, err := Csrf()
	if err != nil {
		t.Fatalf("创建默认 CSRF 中间件失败: %v", err)
	}
	raw := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	raw.AddCookie(&http.Cookie{Name: DefaultCSRFCookieName, Value: "tampered"})
	called := false
	response := handler(fwcontext.MustNewRequest(raw), func(*fwcontext.Request) *fwcontext.Response {
		called = true
		return fwcontext.NewResponse().Content("ok")
	})
	if !called || len(responseCookies(response)) != 1 {
		t.Fatalf("安全请求中的无效 token 应在下游成功后刷新: called=%t cookies=%d", called, len(responseCookies(response)))
	}

	duplicate := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	duplicate.Header.Set("Cookie", DefaultCSRFCookieName+"=first; "+DefaultCSRFCookieName+"=second")
	response = handler(fwcontext.MustNewRequest(duplicate), func(*fwcontext.Request) *fwcontext.Response {
		t.Fatal("重复 CSRF Cookie 的安全请求不应进入下游")
		return nil
	})
	if response.GetStatus() != http.StatusBadRequest {
		t.Fatalf("重复 CSRF Cookie 的安全请求应返回 400，实际为 %d", response.GetStatus())
	}

	nilResponse := handler(
		fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/", nil)),
		func(*fwcontext.Request) *fwcontext.Response { return nil },
	)
	if nilResponse != nil {
		t.Fatalf("下游 nil 响应应保持 nil，实际为 %#v", nilResponse)
	}
}

// TestCSRFRejectsAmbiguousAndMalformedSubmissions 验证重复头、重复表单值和损坏 multipart 都拒绝。
func TestCSRFRejectsAmbiguousAndMalformedSubmissions(t *testing.T) {
	config := DefaultCSRFConfig()
	config.Secret = strings.Repeat("a", 32)
	handler := newCSRFHandler(t, config)
	written := issueCSRFTokenCookie(t, handler)

	duplicateHeader := httptest.NewRequest(http.MethodPost, "https://example.com/save", nil)
	duplicateHeader.AddCookie(written)
	duplicateHeader.Header.Add(config.HeaderName, written.Value)
	duplicateHeader.Header.Add(config.HeaderName, written.Value)

	duplicateForm := httptest.NewRequest(
		http.MethodPost,
		"https://example.com/save",
		strings.NewReader(url.Values{config.FieldName: {written.Value, written.Value}}.Encode()),
	)
	duplicateForm.AddCookie(written)
	duplicateForm.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	malformedType := httptest.NewRequest(http.MethodPost, "https://example.com/save", nil)
	malformedType.AddCookie(written)
	malformedType.Header.Set("Content-Type", `multipart/form-data; boundary="unterminated`)

	malformedMultipart := httptest.NewRequest(http.MethodPost, "https://example.com/save", strings.NewReader("broken"))
	malformedMultipart.AddCookie(written)
	malformedMultipart.Header.Set("Content-Type", "multipart/form-data; boundary=test")

	for _, raw := range []*http.Request{duplicateHeader, duplicateForm, malformedType, malformedMultipart} {
		response := handler(fwcontext.MustNewRequest(raw), func(*fwcontext.Request) *fwcontext.Response {
			t.Fatal("歧义或损坏提交不应进入下游")
			return nil
		})
		if response.GetStatus() != http.StatusForbidden {
			t.Fatalf("歧义或损坏提交应返回 403，实际为 %d", response.GetStatus())
		}
	}
}

// TestCSRFHelperFailuresAreExplicit 验证随机源、读取上限、Cookie 写入和表单值错误均显式返回。
func TestCSRFHelperFailuresAreExplicit(t *testing.T) {
	randomErr := errors.New("random unavailable")
	if _, err := generateCSRFToken(nil); !errors.Is(err, ErrCSRFTokenGeneration) {
		t.Fatalf("nil 随机源应返回 ErrCSRFTokenGeneration，实际为 %v", err)
	}
	if _, err := generateCSRFToken(failingCSRFReader{err: randomErr}); !errors.Is(err, ErrCSRFTokenGeneration) ||
		!errors.Is(err, randomErr) {
		t.Fatalf("随机源错误应完整传播，实际为 %v", err)
	}
	if _, found, err := readCSRFCookie(nil, DefaultCSRFCookieName); found || !errors.Is(err, ErrInvalidCSRFToken) {
		t.Fatalf("nil 请求 Cookie 读取语义错误: found=%t err=%v", found, err)
	}
	oversized := httptest.NewRequest(http.MethodGet, "/", nil)
	oversized.AddCookie(&http.Cookie{Name: DefaultCSRFCookieName, Value: strings.Repeat("x", maxCSRFTokenBytes+1)})
	if _, _, err := readCSRFCookie(oversized, DefaultCSRFCookieName); !errors.Is(err, ErrInvalidCSRFToken) {
		t.Fatalf("超大 CSRF Cookie 应被拒绝，实际为 %v", err)
	}
	if _, err := singleCSRFFormValue([]string{"one", "two"}); !errors.Is(err, ErrInvalidCSRFToken) {
		t.Fatalf("重复表单 token 应被拒绝，实际为 %v", err)
	}
	if value, err := singleCSRFFormValue(nil); err != nil || value != "" {
		t.Fatalf("缺失表单 token 语义错误: value=%q err=%v", value, err)
	}
	if err := setCSRFCookie(nil, nil, DefaultCSRFConfig(), "token", nowForCSRFTest()); !errors.Is(err, ErrInvalidCSRFToken) {
		t.Fatalf("nil 响应应被拒绝，实际为 %v", err)
	}
}

// TestCSRFRejectsNonCanonicalBase64Signature 验证未使用位构造的同字节别名也视为文本篡改。
func TestCSRFRejectsNonCanonicalBase64Signature(t *testing.T) {
	secret := strings.Repeat("c", 32)
	nonce, err := generateCSRFToken(strings.NewReader(strings.Repeat("n", csrfTokenBytes)))
	if err != nil {
		t.Fatalf("生成测试 nonce 失败: %v", err)
	}
	now := nowForCSRFTest()
	token, err := signCSRFToken(DefaultCSRFCookieName, nonce, secret, now)
	if err != nil {
		t.Fatalf("生成测试签名失败: %v", err)
	}
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	lastIndex := strings.IndexByte(alphabet, token[len(token)-1])
	if lastIndex < 0 || lastIndex%4 != 0 {
		t.Fatalf("规范 SHA-256 Base64 尾字符索引错误: %d", lastIndex)
	}
	alias := token[:len(token)-1] + string(alphabet[lastIndex+1])
	if _, err = verifyCSRFToken(DefaultCSRFCookieName, alias, secret, 60, now); !errors.Is(err, ErrInvalidCSRFToken) {
		t.Fatalf("非规范 Base64 签名别名应被拒绝，实际为 %v", err)
	}
}

// TestCSRFHandlerReturns500WhenRandomSourceFails 验证安全请求无法生成 token 时不会静默漏防护。
func TestCSRFHandlerReturns500WhenRandomSourceFails(t *testing.T) {
	config := DefaultCSRFConfig()
	config.Secret = strings.Repeat("b", 32)
	middleware := &csrfMiddleware{
		config: config,
		safeMethods: map[string]struct{}{
			http.MethodGet: {},
		},
		random: failingCSRFReader{err: io.ErrUnexpectedEOF},
		now:    nowForCSRFTest,
	}
	response := middleware.handle(
		fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/", nil)),
		func(*fwcontext.Request) *fwcontext.Response { return fwcontext.NewResponse().Content("ok") },
	)
	if response.GetStatus() != http.StatusInternalServerError {
		t.Fatalf("随机源失败应返回 500，实际为 %d", response.GetStatus())
	}
	if response := middleware.handle(nil, nil); response.GetStatus() != http.StatusInternalServerError {
		t.Fatalf("nil 请求应返回 500，实际为 %d", response.GetStatus())
	}
}

func nowForCSRFTest() time.Time {
	return time.Unix(1_700_000_000, 0)
}
