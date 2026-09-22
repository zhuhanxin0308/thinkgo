package cookie

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type typedNilResponseWriter struct{}

func (*typedNilResponseWriter) Header() http.Header {
	return nil
}

func (*typedNilResponseWriter) Write([]byte) (int, error) {
	return 0, nil
}

func (*typedNilResponseWriter) WriteHeader(int) {}

// TestParseConfigStrictlyValidatesCookiePolicy 验证配置类型、范围、未知字段和安全策略均在启动期收敛。
func TestParseConfigStrictlyValidatesCookiePolicy(t *testing.T) {
	config, err := ParseConfig(map[string]interface{}{
		"prefix":       "think_",
		"secret":       strings.Repeat("s", 32),
		"path":         "/app",
		"domain":       "example.com",
		"secure":       true,
		"httponly":     true,
		"setcookie":    true,
		"samesite":     "Strict",
		"expire":       float64(3600),
		"sign_max_age": float64(1800),
	})
	if err != nil {
		t.Fatalf("合法 Cookie 配置解析失败: %v", err)
	}
	if config.Prefix != "think_" || config.Path != "/app" || config.Domain != "example.com" ||
		!config.Secure || !config.HttpOnly || config.SameSite != "Strict" || config.Expire != 3600 ||
		!config.SetCookie || config.SignMaxAge != 1800 {
		t.Fatalf("Cookie 配置解析结果错误: %#v", config)
	}

	invalid := []map[string]interface{}{
		{"unknown": true},
		{"prefix": "bad;"},
		{"secret": "short"},
		{"path": "relative"},
		{"domain": "bad\ndomain"},
		{"secure": "yes"},
		{"httponly": 1},
		{"setcookie": "yes"},
		{"samesite": "invalid"},
		{"expire": 1.5},
		{"expire": float64(-1)},
		{"sign_max_age": float64(0)},
	}
	for _, raw := range invalid {
		if parsed, parseErr := ParseConfig(raw); !errors.Is(parseErr, ErrInvalidCookieConfig) || parsed != (CookieConfig{}) {
			t.Fatalf("非法 Cookie 配置 %#v 应失败: parsed=%#v err=%v", raw, parsed, parseErr)
		}
	}
}

// TestDefaultConfigMatchesThinkPHPCookieDefaults 验证 Cookie 默认安全属性不擅自
// 覆盖 ThinkPHP 配置，业务可按项目需要显式加强。
func TestDefaultConfigMatchesThinkPHPCookieDefaults(t *testing.T) {
	config, err := ParseConfig(map[string]interface{}{})
	if err != nil {
		t.Fatalf("解析默认 Cookie 配置失败: %v", err)
	}
	if config.Path != "/" || config.Expire != 0 || config.Secure || config.HttpOnly ||
		config.SameSite != "" || !config.SetCookie {
		t.Fatalf("Cookie 默认配置与 ThinkPHP 不一致: %#v", config)
	}
}

// TestSignedCookieRoundTripSupportsArbitraryUTF8 验证签名值安全编码、空值命中和名称绑定。
func TestSignedCookieRoundTripSupportsArbitraryUTF8(t *testing.T) {
	config, err := ParseConfig(map[string]interface{}{
		"prefix": "think_",
		"secret": strings.Repeat("k", 32),
	})
	if err != nil {
		t.Fatalf("解析签名 Cookie 配置失败: %v", err)
	}
	raw := httptest.NewRequest(http.MethodGet, "https://example.com", nil)
	recorder := httptest.NewRecorder()
	writer, err := NewCookieForRequest(config, raw, recorder)
	if err != nil {
		t.Fatalf("创建写 Cookie 实例失败: %v", err)
	}
	original := "你好; value|with.delimiters"
	if err = writer.Set("profile", original); err != nil {
		t.Fatalf("写入签名 Cookie 失败: %v", err)
	}
	if err = writer.Set("empty", ""); err != nil {
		t.Fatalf("写入空 Cookie 失败: %v", err)
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("应写入两个 Cookie，实际为 %d", len(cookies))
	}
	readRequest := httptest.NewRequest(http.MethodGet, "https://example.com", nil)
	for _, item := range cookies {
		readRequest.AddCookie(item)
	}
	reader, err := NewCookieForRequest(config, readRequest, nil)
	if err != nil {
		t.Fatalf("创建读 Cookie 实例失败: %v", err)
	}
	value, found, err := reader.Get("profile")
	if err != nil || !found || value != original {
		t.Fatalf("签名 Cookie 往返错误: value=%q found=%t err=%v", value, found, err)
	}
	value, found, err = reader.Get("empty")
	if err != nil || !found || value != "" {
		t.Fatalf("空 Cookie 命中语义错误: value=%q found=%t err=%v", value, found, err)
	}
	if exists, hasErr := reader.Has("profile"); hasErr != nil || !exists {
		t.Fatalf("Has 未识别有效签名 Cookie: exists=%t err=%v", exists, hasErr)
	}

	profileCookie := cookies[0]
	profileCookie.Name = "think_other"
	swappedRequest := httptest.NewRequest(http.MethodGet, "https://example.com", nil)
	swappedRequest.AddCookie(profileCookie)
	swappedReader, _ := NewCookieForRequest(config, swappedRequest, nil)
	if _, _, err = swappedReader.Get("other"); !errors.Is(err, ErrInvalidCookieSignature) {
		t.Fatalf("签名值跨名称调换应失败，实际为 %v", err)
	}
}

// TestSignedCookieRejectsTamperingFutureAndExpiredTimestamp 验证签名篡改、未来时间和过期重放均失败。
func TestSignedCookieRejectsTamperingFutureAndExpiredTimestamp(t *testing.T) {
	config := DefaultConfig()
	config.Secret = strings.Repeat("z", 32)
	config.SignMaxAge = 60
	now := time.Now().Truncate(time.Second)
	valid, err := signCookieValue("sid", "value", config.Secret, now)
	if err != nil {
		t.Fatalf("生成测试签名失败: %v", err)
	}
	if _, err = unsignCookieValue("sid", valid+"x", config.Secret, config.SignMaxAge, now); !errors.Is(err, ErrInvalidCookieSignature) {
		t.Fatalf("篡改签名应返回 ErrInvalidCookieSignature，实际为 %v", err)
	}
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	lastIndex := strings.IndexByte(alphabet, valid[len(valid)-1])
	if lastIndex < 0 || lastIndex%4 != 0 {
		t.Fatalf("规范 SHA-256 Base64 尾字符索引错误: %d", lastIndex)
	}
	alias := valid[:len(valid)-1] + string(alphabet[lastIndex+1])
	if _, err = unsignCookieValue("sid", alias, config.Secret, config.SignMaxAge, now); !errors.Is(err, ErrInvalidCookieSignature) {
		t.Fatalf("非规范 Base64 签名别名应被拒绝，实际为 %v", err)
	}
	future, _ := signCookieValue("sid", "value", config.Secret, now.Add(maxCookieClockSkew+time.Second))
	if _, err = unsignCookieValue("sid", future, config.Secret, config.SignMaxAge, now); !errors.Is(err, ErrInvalidCookieSignature) {
		t.Fatalf("未来签名应被拒绝，实际为 %v", err)
	}
	expired, _ := signCookieValue("sid", "value", config.Secret, now.Add(-61*time.Second))
	if _, err = unsignCookieValue("sid", expired, config.Secret, config.SignMaxAge, now); !errors.Is(err, ErrExpiredCookieSignature) {
		t.Fatalf("过期签名应返回 ErrExpiredCookieSignature，实际为 %v", err)
	}
}

// TestCookieRejectsDuplicateAndInvalidIO 验证重复名称、缺失 writer、非法名称和值不会静默成功。
func TestCookieRejectsDuplicateAndInvalidIO(t *testing.T) {
	config := DefaultConfig()
	raw := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
	raw.Header.Add("Cookie", "sid=first; sid=second")
	reader, err := NewCookieForRequest(config, raw, nil)
	if err != nil {
		t.Fatalf("创建重复 Cookie 读取器失败: %v", err)
	}
	if _, _, err = reader.Get("sid"); !errors.Is(err, ErrDuplicateCookie) {
		t.Fatalf("重复 Cookie 应返回 ErrDuplicateCookie，实际为 %v", err)
	}
	if err = reader.Set("sid", "value"); !errors.Is(err, ErrCookieWriterUnavailable) {
		t.Fatalf("缺失 writer 应返回 ErrCookieWriterUnavailable，实际为 %v", err)
	}

	recorder := httptest.NewRecorder()
	writer, _ := NewCookieForRequest(config, raw, recorder)
	if err = writer.Set("bad name", "value"); !errors.Is(err, ErrInvalidCookieName) {
		t.Fatalf("非法 Cookie 名应返回 ErrInvalidCookieName，实际为 %v", err)
	}
	if err = writer.Set("sid", "bad\nvalue"); !errors.Is(err, ErrInvalidCookieValue) {
		t.Fatalf("非法 Cookie 值应返回 ErrInvalidCookieValue，实际为 %v", err)
	}
	if err = writer.Set("sid", strings.Repeat("x", maxCookieHeaderBytes)); !errors.Is(err, ErrCookieTooLarge) {
		t.Fatalf("超大 Cookie 应返回 ErrCookieTooLarge，实际为 %v", err)
	}
	if err = writer.Set("sid", "value", CookieOptions{}, CookieOptions{}); !errors.Is(err, ErrInvalidCookieOptions) {
		t.Fatalf("多个 options 应返回 ErrInvalidCookieOptions，实际为 %v", err)
	}
}

// TestCookieSecurePolicyAndDelete 验证可信 HTTPS 判断、SameSite=None 强制 Secure 和删除属性。
func TestCookieSecurePolicyAndDelete(t *testing.T) {
	config := DefaultConfig()
	loopback := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
	loopback.RemoteAddr = "127.0.0.1:54321"
	loopback.Header.Set("X-Forwarded-Proto", "https")
	recorder := httptest.NewRecorder()
	manager, _ := NewCookieForRequest(config, loopback, recorder)
	if err := manager.Set("sid", "abc"); err != nil {
		t.Fatalf("写入可信代理 Cookie 失败: %v", err)
	}
	if !recorder.Result().Cookies()[0].Secure {
		t.Fatal("本机 HTTPS 反代后的 Cookie 应自动开启 Secure")
	}

	remote := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
	remote.RemoteAddr = "198.51.100.10:54321"
	remote.Header.Set("X-Forwarded-Proto", "https")
	remoteRecorder := httptest.NewRecorder()
	remoteManager, _ := NewCookieForRequest(config, remote, remoteRecorder)
	if err := remoteManager.Set("sid", "abc", CookieOptions{
		Path: "/", HttpOnly: true, SameSite: "None",
	}); err != nil {
		t.Fatalf("写入 SameSite=None Cookie 失败: %v", err)
	}
	if err := remoteManager.Delete("sid"); err != nil {
		t.Fatalf("删除 Cookie 失败: %v", err)
	}
	remoteCookies := remoteRecorder.Result().Cookies()
	if len(remoteCookies) != 2 {
		t.Fatalf("应写入设置与删除两个 Cookie，实际为 %d", len(remoteCookies))
	}
	if !remoteCookies[0].Secure {
		t.Fatal("SameSite=None 必须强制 Secure")
	}
	deleted := remoteCookies[1]
	if deleted.MaxAge != -1 || !deleted.Expires.Before(time.Now()) {
		t.Fatalf("删除 Cookie 属性错误: %#v", deleted)
	}
}

// TestCookieExplicitSecureOverride 验证框架传入的可信协议结论可以覆盖旧的请求级自动推断。
func TestCookieExplicitSecureOverride(t *testing.T) {
	spoofed := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
	spoofed.RemoteAddr = "127.0.0.1:54321"
	spoofed.Header.Set("X-Forwarded-Proto", "https")
	unsafeRecorder := httptest.NewRecorder()
	unsafe, err := NewCookieForRequestWithSecure(DefaultConfig(), spoofed, unsafeRecorder, false)
	if err != nil {
		t.Fatalf("创建显式非安全 Cookie 失败: %v", err)
	}
	if err = unsafe.Set("sid", "value"); err != nil {
		t.Fatalf("写入显式非安全 Cookie 失败: %v", err)
	}
	if cookies := unsafeRecorder.Result().Cookies(); len(cookies) != 1 || cookies[0].Secure {
		t.Fatalf("不可信协议结论不应被伪造头提升为 Secure: %#v", cookies)
	}

	secureRecorder := httptest.NewRecorder()
	secure, err := NewCookieForRequestWithSecure(DefaultConfig(), httptest.NewRequest(http.MethodGet, "http://example.com", nil), secureRecorder, true)
	if err != nil {
		t.Fatalf("创建显式安全 Cookie 失败: %v", err)
	}
	if err = secure.Set("sid", "value"); err != nil {
		t.Fatalf("写入显式安全 Cookie 失败: %v", err)
	}
	if cookies := secureRecorder.Result().Cookies(); len(cookies) != 1 || !cookies[0].Secure {
		t.Fatalf("可信协议结论应开启 Secure: %#v", cookies)
	}
}

// TestCookieFactoryCopiesImmutableConfig 验证请求级实例复用配置快照且不修改工厂。
func TestCookieFactoryCopiesImmutableConfig(t *testing.T) {
	factory, err := NewCookie(map[string]interface{}{"path": "/", "httponly": true})
	if err != nil {
		t.Fatalf("创建 Cookie 工厂失败: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
	requestCookie, err := factory.ForRequest(request, httptest.NewRecorder())
	if err != nil {
		t.Fatalf("创建请求 Cookie 失败: %v", err)
	}
	if requestCookie.GetConfig() != factory.GetConfig() {
		t.Fatal("请求 Cookie 未复用不可变配置快照")
	}
	if _, err = NewCookieForRequest(DefaultConfig(), nil, nil); !errors.Is(err, ErrCookieRequestUnavailable) {
		t.Fatalf("nil 请求应返回 ErrCookieRequestUnavailable，实际为 %v", err)
	}
}

// TestUnsignedCookieLifecycle 验证未签名 Cookie 也能区分缺失与空值，并显式处理 writer 与覆盖选项。
func TestUnsignedCookieLifecycle(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
	request.RemoteAddr = "198.51.100.10:1234"
	request.Header.Set("X-Forwarded-Proto", "https")
	manager, err := NewCookieForRequest(DefaultConfig(), request, nil)
	if err != nil {
		t.Fatalf("创建未签名 Cookie 失败: %v", err)
	}
	if value, found, getErr := manager.Get("missing"); getErr != nil || found || value != "" {
		t.Fatalf("缺失 Cookie 语义错误: value=%q found=%t err=%v", value, found, getErr)
	}
	recorder := httptest.NewRecorder()
	if err = manager.SetWriter(recorder); err != nil {
		t.Fatalf("绑定 Cookie writer 失败: %v", err)
	}
	if err = manager.Set("sid", "plain", CookieOptions{
		Path: "/account", SameSite: "Strict", Expire: 120,
	}); err != nil {
		t.Fatalf("写入未签名 Cookie 失败: %v", err)
	}
	written := recorder.Result().Cookies()
	if len(written) != 1 || written[0].Value != "plain" || written[0].Path != "/account" ||
		written[0].MaxAge != 120 || written[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("未签名 Cookie 属性错误: %#v", written)
	}
	if written[0].Secure {
		t.Fatal("非可信远端的 X-Forwarded-Proto 不得开启 Secure 推断")
	}

	readRequest := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
	readRequest.AddCookie(written[0])
	reader, _ := NewCookieForRequest(DefaultConfig(), readRequest, nil)
	if value, found, getErr := reader.Get("sid"); getErr != nil || !found || value != "plain" {
		t.Fatalf("未签名 Cookie 往返错误: value=%q found=%t err=%v", value, found, getErr)
	}
	if err = manager.Set("sid", "plain", CookieOptions{Path: "relative"}); !errors.Is(err, ErrInvalidCookieOptions) {
		t.Fatalf("非法覆盖选项应返回 ErrInvalidCookieOptions，实际为 %v", err)
	}
	if err = manager.SetWriter(nil); !errors.Is(err, ErrCookieWriterUnavailable) {
		t.Fatalf("nil writer 应返回 ErrCookieWriterUnavailable，实际为 %v", err)
	}
}

// TestCookieFactoriesAndSignaturesRejectInvalidState 验证非法工厂状态与畸形签名不会被静默接受。
func TestCookieFactoriesAndSignaturesRejectInvalidState(t *testing.T) {
	if _, err := NewCookieWithConfig(CookieConfig{}); !errors.Is(err, ErrInvalidCookieConfig) {
		t.Fatalf("零值配置应被拒绝，实际为 %v", err)
	}
	var nilFactory *Cookie
	if _, err := nilFactory.ForRequest(httptest.NewRequest(http.MethodGet, "/", nil), nil); !errors.Is(err, ErrInvalidCookieConfig) {
		t.Fatalf("nil 工厂应返回 ErrInvalidCookieConfig，实际为 %v", err)
	}
	if _, _, err := nilFactory.Get("sid"); !errors.Is(err, ErrCookieRequestUnavailable) {
		t.Fatalf("nil Cookie 读取应返回 ErrCookieRequestUnavailable，实际为 %v", err)
	}
	if err := nilFactory.Set("sid", "value"); !errors.Is(err, ErrCookieWriterUnavailable) {
		t.Fatalf("nil Cookie 写入应返回 ErrCookieWriterUnavailable，实际为 %v", err)
	}

	secret := strings.Repeat("s", 32)
	now := time.Now().Truncate(time.Second)
	timestamp := strconv.FormatInt(now.Unix(), 10)
	malformed := []string{
		"v0.payload.1.signature",
		"v1.payload.not-a-time.signature",
		"v1.***." + timestamp + ".***",
		"v1.payload." + timestamp + ".short",
	}
	for _, signed := range malformed {
		if _, err := unsignCookieValue("sid", signed, secret, 60, now); !errors.Is(err, ErrInvalidCookieSignature) {
			t.Fatalf("畸形签名 %q 应被拒绝，实际为 %v", signed, err)
		}
	}
	if _, err := signCookieValue("sid", "value", "short", now); !errors.Is(err, ErrInvalidCookieSignature) {
		t.Fatalf("短密钥签名应失败，实际为 %v", err)
	}
}

// TestCookieRejectsTypedNilWriter 验证承载 nil 指针的 ResponseWriter 不会绕过可用性检查触发 panic。
func TestCookieRejectsTypedNilWriter(t *testing.T) {
	var typedNil *typedNilResponseWriter
	manager, err := NewCookieForRequest(DefaultConfig(), httptest.NewRequest(http.MethodGet, "/", nil), typedNil)
	if err != nil {
		t.Fatalf("typed nil writer 不应影响请求级 Cookie 创建，实际为 %v", err)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("typed nil writer 不应触发 panic: %v", recovered)
		}
	}()
	if err := manager.Set("sid", "value"); !errors.Is(err, ErrCookieWriterUnavailable) {
		t.Fatalf("typed nil writer 应返回 ErrCookieWriterUnavailable，实际为 %v", err)
	}
	if err := manager.SetWriter(typedNil); !errors.Is(err, ErrCookieWriterUnavailable) {
		t.Fatalf("绑定 typed nil writer 应返回 ErrCookieWriterUnavailable，实际为 %v", err)
	}
}

// TestCookieConcurrentWritesAreSerialized 验证请求级 Cookie 并发写响应头不会触发 map 竞争或丢失字段。
func TestCookieConcurrentWritesAreSerialized(t *testing.T) {
	recorder := httptest.NewRecorder()
	manager, err := NewCookieForRequest(DefaultConfig(), httptest.NewRequest(http.MethodGet, "http://example.com", nil), recorder)
	if err != nil {
		t.Fatalf("创建并发 Cookie 实例失败: %v", err)
	}
	const workers = 50
	var wg sync.WaitGroup
	errorsChannel := make(chan error, workers)
	for index := 0; index < workers; index++ {
		wg.Add(1)
		go func(value int) {
			defer wg.Done()
			errorsChannel <- manager.Set("item_"+strconv.Itoa(value), "value")
		}(index)
	}
	wg.Wait()
	close(errorsChannel)
	for writeErr := range errorsChannel {
		if writeErr != nil {
			t.Fatalf("并发写 Cookie 失败: %v", writeErr)
		}
	}
	if actual := len(recorder.Header().Values("Set-Cookie")); actual != workers {
		t.Fatalf("并发 Cookie 写入发生丢失: actual=%d expected=%d", actual, workers)
	}
}

// TestCookieBuildHeaderSupportsValidatedInMemoryResponses 验证控制器无需原生 writer 也能取得完整校验后的头值。
func TestCookieBuildHeaderSupportsValidatedInMemoryResponses(t *testing.T) {
	manager, err := NewCookieForRequest(
		DefaultConfig(), httptest.NewRequest(http.MethodGet, "https://example.com", nil), nil,
	)
	if err != nil {
		t.Fatalf("创建内存响应 Cookie 实例失败: %v", err)
	}
	header, err := manager.BuildHeader("token", "value", CookieOptions{
		Path: "/", HttpOnly: true, SameSite: "Strict", Expire: 60,
	})
	if err != nil {
		t.Fatalf("构建 Set-Cookie 头失败: %v", err)
	}
	response := &http.Response{Header: http.Header{"Set-Cookie": []string{header}}}
	cookies := response.Cookies()
	if len(cookies) != 1 || cookies[0].Name != "token" || cookies[0].Value != "value" || !cookies[0].Secure {
		t.Fatalf("构建的 Set-Cookie 头属性错误: header=%q cookies=%#v", header, cookies)
	}
	if _, err = manager.BuildHeader("bad name", "value"); !errors.Is(err, ErrInvalidCookieName) {
		t.Fatalf("BuildHeader 必须复用名称校验，实际为 %v", err)
	}
}
