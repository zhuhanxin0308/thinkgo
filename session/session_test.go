package session

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/cookie"
)

// countingDriver 提供线程安全的可观测原子存储，专门验证 Session 的持久化协议。
type countingDriver struct {
	mu          sync.Mutex
	data        map[string]string
	readErr     error
	writeErr    error
	deleteErr   error
	updateErr   error
	readCount   int
	writeCount  int
	deleteCount int
	updateCount int
	lastWriteID string
}

func newCountingDriver() *countingDriver {
	return &countingDriver{data: make(map[string]string)}
}

func (d *countingDriver) Read(id string) (string, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.readCount++
	if d.readErr != nil {
		return "", false, d.readErr
	}
	value, found := d.data[id]
	return value, found, nil
}

func (d *countingDriver) Write(id string, data string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.writeCount++
	if d.writeErr != nil {
		return d.writeErr
	}
	d.data[id] = data
	d.lastWriteID = id
	return nil
}

func (d *countingDriver) Delete(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.deleteCount++
	if d.deleteErr != nil {
		return d.deleteErr
	}
	delete(d.data, id)
	return nil
}

func (d *countingDriver) Clear() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.data = make(map[string]string)
	return nil
}

func (d *countingDriver) Update(id string, update func(string, bool) (string, bool, error)) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.updateCount++
	if d.updateErr != nil {
		return d.updateErr
	}
	current, found := d.data[id]
	next, remove, err := update(current, found)
	if err != nil {
		return err
	}
	if remove {
		delete(d.data, id)
		return nil
	}
	d.data[id] = next
	d.lastWriteID = id
	return nil
}

func (d *countingDriver) snapshot(id string) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	value, found := d.data[id]
	return value, found
}

// newTestSessionManager 创建完全内存化且依赖经过校验的 Session 管理器。
func newTestSessionManager(t *testing.T, driver Driver, sessionConfig, cookieConfig map[string]interface{}) *Session {
	t.Helper()
	cookieFactory, err := cookie.NewCookie(cookieConfig)
	if err != nil {
		t.Fatalf("创建测试 Cookie 工厂失败: %v", err)
	}
	manager, err := NewSession(sessionConfig, driver, cookieFactory)
	if err != nil {
		t.Fatalf("创建测试 Session 管理器失败: %v", err)
	}
	return manager
}

// TestParseConfigStrictlyValidatesSessionPolicy 验证未知字段、旧歧义字段和不精确数值均在启动期失败。
func TestParseConfigStrictlyValidatesSessionPolicy(t *testing.T) {
	config, err := ParseConfig(map[string]interface{}{
		"name":           "SID",
		"type":           "file",
		"storage_path":   "./runtime/session",
		"cookie_path":    "/account",
		"expire":         float64(3600),
		"domain":         "example.com",
		"secure":         true,
		"httponly":       true,
		"samesite":       "Strict",
		"max_data_bytes": float64(32768),
		"max_entries":    float64(2048),
	})
	if err != nil {
		t.Fatalf("合法 Session 配置解析失败: %v", err)
	}
	if config.Name != "SID" || config.DriverType != "file" || config.CookiePath != "/account" ||
		config.Expire != 3600 || config.MaxDataBytes != 32768 || config.MaxEntries != 2048 || !config.Secure {
		t.Fatalf("Session 配置解析结果错误: %#v", config)
	}
	redisConfig, err := ParseConfig(map[string]interface{}{
		"type": "redis",
		"redis": map[string]interface{}{
			"host": "127.0.0.1", "port": 6379, "prefix": "thinkgo:test:session:",
		},
	})
	if err != nil || redisConfig.DriverType != "redis" {
		t.Fatalf("合法 Redis Session 配置解析失败: config=%#v err=%v", redisConfig, err)
	}

	invalid := []map[string]interface{}{
		{"unknown": true},
		{"path": "./runtime/session"},
		{"name": "bad name"},
		{"type": "redis"},
		{"storage_path": ""},
		{"cookie_path": "relative"},
		{"expire": 1.5},
		{"expire": -1},
		{"secure": "yes"},
		{"httponly": false},
		{"samesite": "invalid"},
		{"max_data_bytes": 0},
		{"max_entries": 0},
	}
	for _, raw := range invalid {
		if parsed, parseErr := ParseConfig(raw); !errors.Is(parseErr, ErrInvalidSessionConfig) || parsed != (Config{}) {
			t.Fatalf("非法 Session 配置 %#v 应失败: parsed=%#v err=%v", raw, parsed, parseErr)
		}
	}
}

// TestDefaultSessionConfigMatchesThinkPHP 验证未显式配置时使用 ThinkPHP
// 的 PHPSESSID、file 驱动和 1440 秒有效期。
func TestDefaultSessionConfigMatchesThinkPHP(t *testing.T) {
	config := DefaultConfig()
	if config.DriverType != "file" || config.Name != "PHPSESSID" || config.Expire != 1440 || config.VarSessionID != "" {
		t.Fatalf("Session 默认配置与 ThinkPHP 不一致: %#v", config)
	}
}

// TestGeneratedSessionIDMatchesThinkPHPStore 验证默认 ID 为 32 位字母数字串。
func TestGeneratedSessionIDMatchesThinkPHPStore(t *testing.T) {
	manager := newTestSessionManager(t, newCountingDriver(), map[string]interface{}{"name": "SID"}, nil)
	requestSession, err := manager.NewRequestSession(httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder())
	if err != nil {
		t.Fatalf("创建请求 Session 失败: %v", err)
	}
	if len(requestSession.id) != 32 || !isSafeSessionID(requestSession.id) {
		t.Fatalf("Session ID 与 ThinkPHP Store 不一致: %q", requestSession.id)
	}
}

// TestSaveSkipsUntouchedSession 验证未使用的 Session 不产生存储写入和 Cookie。
func TestSaveSkipsUntouchedSession(t *testing.T) {
	driver := newCountingDriver()
	manager := newTestSessionManager(t, driver, map[string]interface{}{"name": "SID"}, nil)
	recorder := httptest.NewRecorder()
	reqSession, err := manager.NewRequestSession(httptest.NewRequest(http.MethodGet, "http://example.com/", nil), recorder)
	if err != nil {
		t.Fatalf("初始化请求 Session 失败: %v", err)
	}
	if err = reqSession.Save(); err != nil {
		t.Fatalf("保存未使用 Session 失败: %v", err)
	}
	if driver.updateCount != 0 || len(recorder.Result().Cookies()) != 0 {
		t.Fatalf("未使用 Session 不应产生副作用: update=%d cookies=%d", driver.updateCount, len(recorder.Result().Cookies()))
	}
}

// TestSetSameValueSkipsPersistence 验证幂等 Set 不会重复制造持久化写入，降低文件 Session 的无效 I/O。
func TestSetSameValueSkipsPersistence(t *testing.T) {
	driver := newCountingDriver()
	manager := newTestSessionManager(t, driver, map[string]interface{}{"name": "SID", "expire": 0}, nil)
	recorder := httptest.NewRecorder()
	reqSession, err := manager.NewRequestSession(httptest.NewRequest(http.MethodGet, "http://example.com/", nil), recorder)
	if err != nil {
		t.Fatalf("创建请求 Session 失败: %v", err)
	}
	if err = reqSession.Set("role", "admin"); err != nil {
		t.Fatalf("首次设置 Session 失败: %v", err)
	}
	if err = reqSession.Save(); err != nil {
		t.Fatalf("首次保存 Session 失败: %v", err)
	}
	writes := driver.updateCount
	if err = reqSession.Set("role", "admin"); err != nil {
		t.Fatalf("重复设置 Session 失败: %v", err)
	}
	if err = reqSession.Save(); err != nil {
		t.Fatalf("重复设置后保存 Session 失败: %v", err)
	}
	if driver.updateCount != writes {
		t.Fatalf("相同 Session 值不应再次持久化: before=%d after=%d", writes, driver.updateCount)
	}
}

// TestSetSameValueRefreshesExpiringSession 验证带 TTL 的 Session 仍通过重复 Set 刷新服务端过期时间。
func TestSetSameValueRefreshesExpiringSession(t *testing.T) {
	driver := newCountingDriver()
	manager := newTestSessionManager(t, driver, map[string]interface{}{"name": "SID", "expire": 60}, nil)
	reqSession, err := manager.NewRequestSession(httptest.NewRequest(http.MethodGet, "http://example.com/", nil), httptest.NewRecorder())
	if err != nil {
		t.Fatalf("创建请求 Session 失败: %v", err)
	}
	if err = reqSession.Set("role", "admin"); err != nil {
		t.Fatalf("首次设置 Session 失败: %v", err)
	}
	if err = reqSession.Save(); err != nil {
		t.Fatalf("首次保存 Session 失败: %v", err)
	}
	writes := driver.updateCount
	if err = reqSession.Set("role", "admin"); err != nil {
		t.Fatalf("重复设置带 TTL 的 Session 失败: %v", err)
	}
	if err = reqSession.Save(); err != nil {
		t.Fatalf("重复设置带 TTL 的 Session 保存失败: %v", err)
	}
	if driver.updateCount != writes+1 {
		t.Fatalf("带 TTL 的相同值 Set 必须刷新持久化记录: before=%d after=%d", writes, driver.updateCount)
	}
}

// TestSaveUsesSessionCookiePolicy 验证 Session 配置完整映射到最终 Cookie。
func TestSaveUsesSessionCookiePolicy(t *testing.T) {
	driver := newCountingDriver()
	manager := newTestSessionManager(t, driver, map[string]interface{}{
		"name": "SID", "expire": 600, "cookie_path": "/admin", "domain": "example.com",
		"secure": true, "httponly": true, "samesite": "Strict",
	}, nil)
	recorder := httptest.NewRecorder()
	reqSession, err := manager.NewRequestSession(httptest.NewRequest(http.MethodGet, "https://example.com/admin", nil), recorder)
	if err != nil {
		t.Fatalf("初始化请求 Session 失败: %v", err)
	}
	if err = reqSession.Set("role", "admin"); err != nil {
		t.Fatalf("设置 Session 数据失败: %v", err)
	}
	if err = reqSession.Save(); err != nil {
		t.Fatalf("保存 Session 失败: %v", err)
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("应下发一个 Session Cookie，实际为 %d", len(cookies))
	}
	written := cookies[0]
	if written.Path != "/admin" || written.Domain != "example.com" || !written.Secure || !written.HttpOnly ||
		written.SameSite != http.SameSiteStrictMode || written.MaxAge != 600 {
		t.Fatalf("Session Cookie 属性错误: %#v", written)
	}
}

// TestSessionExplicitSecurePolicy 验证 Session 中间件传入的协议结论不会被原始请求头重新推断。
func TestSessionExplicitSecurePolicy(t *testing.T) {
	manager := newTestSessionManager(t, newCountingDriver(), map[string]interface{}{"name": "SID"}, nil)
	spoofed := httptest.NewRequest(http.MethodPost, "http://example.com/login", nil)
	spoofed.RemoteAddr = "127.0.0.1:4321"
	spoofed.Header.Set("X-Forwarded-Proto", "https")
	unsafeRecorder := httptest.NewRecorder()
	unsafe, err := manager.NewRequestSessionWithSecure(spoofed, unsafeRecorder, false)
	if err != nil {
		t.Fatalf("创建显式非安全 Session 失败: %v", err)
	}
	if err = unsafe.Set("uid", 1); err != nil {
		t.Fatalf("设置显式非安全 Session 失败: %v", err)
	}
	if err = unsafe.Save(); err != nil {
		t.Fatalf("保存显式非安全 Session 失败: %v", err)
	}
	if cookies := unsafeRecorder.Result().Cookies(); len(cookies) != 1 || cookies[0].Secure {
		t.Fatalf("不可信协议结论不应产生 Secure Session Cookie: %#v", cookies)
	}

	secureRecorder := httptest.NewRecorder()
	secure, err := manager.NewRequestSessionWithSecure(httptest.NewRequest(http.MethodPost, "http://example.com/login", nil), secureRecorder, true)
	if err != nil {
		t.Fatalf("创建显式安全 Session 失败: %v", err)
	}
	if err = secure.Set("uid", 1); err != nil {
		t.Fatalf("设置显式安全 Session 失败: %v", err)
	}
	if err = secure.Save(); err != nil {
		t.Fatalf("保存显式安全 Session 失败: %v", err)
	}
	if cookies := secureRecorder.Result().Cookies(); len(cookies) != 1 || !cookies[0].Secure {
		t.Fatalf("可信协议结论应产生 Secure Session Cookie: %#v", cookies)
	}
}

// TestUnknownAndUnsafeSessionIDsAreNeverReused 验证客户端不能固定安全格式或路径型 Session ID。
func TestUnknownAndUnsafeSessionIDsAreNeverReused(t *testing.T) {
	for _, supplied := range []string{"fixed-session-id", "../secret.txt"} {
		driver := newCountingDriver()
		manager := newTestSessionManager(t, driver, map[string]interface{}{"name": "SID"}, nil)
		raw := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
		raw.AddCookie(&http.Cookie{Name: "SID", Value: supplied})
		recorder := httptest.NewRecorder()
		reqSession, err := manager.NewRequestSession(raw, recorder)
		if err != nil {
			t.Fatalf("替换外部 Session ID 失败: %v", err)
		}
		if err = reqSession.Set("uid", 1); err != nil {
			t.Fatalf("设置 Session 失败: %v", err)
		}
		if err = reqSession.Save(); err != nil {
			t.Fatalf("保存替换后的 Session 失败: %v", err)
		}
		if reqSession.id == supplied || driver.lastWriteID == supplied || strings.Contains(driver.lastWriteID, "..") {
			t.Fatalf("外部 Session ID 被错误复用: supplied=%q actual=%q", supplied, driver.lastWriteID)
		}
	}
}

// TestServerExpiryInvalidatesPersistedSession 验证服务端过期时间不依赖客户端 Cookie。
func TestServerExpiryInvalidatesPersistedSession(t *testing.T) {
	driver := newCountingDriver()
	manager := newTestSessionManager(t, driver, map[string]interface{}{"name": "SID", "expire": 60}, nil)
	now := time.Unix(1_700_000_000, 0)
	manager.now = func() time.Time { return now }
	firstRecorder := httptest.NewRecorder()
	first, err := manager.NewRequestSession(httptest.NewRequest(http.MethodGet, "http://example.com/", nil), firstRecorder)
	if err != nil {
		t.Fatalf("初始化首次 Session 失败: %v", err)
	}
	if err = first.Set("uid", 7); err != nil {
		t.Fatalf("设置首次 Session 失败: %v", err)
	}
	if err = first.Save(); err != nil {
		t.Fatalf("保存首次 Session 失败: %v", err)
	}
	now = now.Add(61 * time.Second)
	raw := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	raw.AddCookie(firstRecorder.Result().Cookies()[0])
	second, err := manager.NewRequestSession(raw, httptest.NewRecorder())
	if err != nil {
		t.Fatalf("过期 Session 应安全降级为新会话: %v", err)
	}
	if _, found := second.Get("uid"); found || second.id == first.id {
		t.Fatalf("已过期 Session 不应恢复或复用: found=%t old=%q new=%q", found, first.id, second.id)
	}
}

// TestSessionRejectsInvalidKeysValuesAndOversizedData 验证非法输入在写入内存状态前失败。
func TestSessionRejectsInvalidKeysValuesAndOversizedData(t *testing.T) {
	manager := newTestSessionManager(t, newCountingDriver(), map[string]interface{}{
		"name": "SID", "max_data_bytes": 256,
	}, nil)
	reqSession, err := manager.NewRequestSession(httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder())
	if err != nil {
		t.Fatalf("初始化请求 Session 失败: %v", err)
	}
	if err = reqSession.Set("", "value"); !errors.Is(err, ErrInvalidSessionKey) {
		t.Fatalf("空键应返回 ErrInvalidSessionKey，实际为 %v", err)
	}
	if err = reqSession.Set("stream", make(chan int)); !errors.Is(err, ErrInvalidSessionValue) {
		t.Fatalf("不可序列化值应返回 ErrInvalidSessionValue，实际为 %v", err)
	}
	if err = reqSession.Set("large", strings.Repeat("x", 512)); !errors.Is(err, ErrSessionDataTooLarge) {
		t.Fatalf("超大值应返回 ErrSessionDataTooLarge，实际为 %v", err)
	}
	if reqSession.Has("stream") || reqSession.Has("large") {
		t.Fatal("非法值不得污染 Session 内存状态")
	}
}

// TestCorruptBackendDataIsReported 验证损坏存储不会被当成空会话静默掩盖。
func TestCorruptBackendDataIsReported(t *testing.T) {
	driver := newCountingDriver()
	const corruptSessionID = "fedcba9876543210fedcba9876543210"
	driver.data[corruptSessionID] = `{"version":1,"data":`
	manager := newTestSessionManager(t, driver, map[string]interface{}{"name": "SID"}, nil)
	raw := httptest.NewRequest(http.MethodGet, "/", nil)
	raw.AddCookie(&http.Cookie{Name: "SID", Value: corruptSessionID})
	if _, err := manager.NewRequestSession(raw, httptest.NewRecorder()); !errors.Is(err, ErrCorruptSession) {
		t.Fatalf("损坏 Session 应返回 ErrCorruptSession，实际为 %v", err)
	}
}

// TestSessionJSONValuesAreIsolated 验证 Set/Get 不暴露可变引用，避免绕过 dirty 跟踪。
func TestSessionJSONValuesAreIsolated(t *testing.T) {
	manager := newTestSessionManager(t, newCountingDriver(), map[string]interface{}{"name": "SID"}, nil)
	reqSession, err := manager.NewRequestSession(httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder())
	if err != nil {
		t.Fatalf("初始化请求 Session 失败: %v", err)
	}
	original := map[string]interface{}{"roles": []string{"user"}}
	if err = reqSession.Set("profile", original); err != nil {
		t.Fatalf("设置复合值失败: %v", err)
	}
	original["roles"] = []string{"admin"}
	value, found := reqSession.Get("profile")
	if !found {
		t.Fatal("已设置的复合值应存在")
	}
	encoded, _ := json.Marshal(value)
	if string(encoded) != `{"roles":["user"]}` {
		t.Fatalf("外部修改污染了 Session: %s", encoded)
	}
}
