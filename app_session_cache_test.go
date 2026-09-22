package framework

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/cache"
	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
	frameworkCookie "github.com/zhuhanxin0308/thinkgo/v3/cookie"
	frameworkSession "github.com/zhuhanxin0308/thinkgo/v3/session"
)

type expiringCacheSessionLockDriver struct {
	*cacheDriver.Memory
	lease time.Duration
}

func (driver *expiringCacheSessionLockDriver) AcquireLock(key, owner string, _ time.Duration) (bool, error) {
	return driver.Memory.AcquireLock(key, owner, driver.lease)
}

// TestCacheSessionDriverThinkPHPPersistenceAPI 验证 session.type=cache 的读取、
// 写入、删除、清空和原子更新都通过同一个已配置 Cache store。
func TestCacheSessionDriverThinkPHPPersistenceAPI(t *testing.T) {
	store := cache.NewCache(nil, cacheDriver.NewMemory())
	driver := &cacheSessionDriver{store: store, expire: time.Minute}

	if value, found, err := driver.Read("missing"); err != nil || found || value != "" {
		t.Fatalf("缺失 Session 读取错误: value=%q found=%t err=%v", value, found, err)
	}
	if err := driver.Write("session-1", `{"uid":7}`); err != nil {
		t.Fatalf("写入缓存 Session 失败: %v", err)
	}
	if value, found, err := driver.ReadContext(context.Background(), "session-1"); err != nil || !found || value != `{"uid":7}` {
		t.Fatalf("读取缓存 Session 错误: value=%q found=%t err=%v", value, found, err)
	}
	if err := store.Set(driver.key("invalid"), 7, time.Minute); err != nil {
		t.Fatalf("准备非法 Session 缓存值失败: %v", err)
	}
	if _, found, err := driver.Read("invalid"); err == nil || found {
		t.Fatalf("非字符串 Session 缓存必须拒绝: found=%t err=%v", found, err)
	}

	if err := driver.Update("session-1", func(current string, found bool) (string, bool, error) {
		if !found || current != `{"uid":7}` {
			return "", false, errors.New("更新未读取现有 Session")
		}
		return `{"uid":8}`, false, nil
	}); err != nil {
		t.Fatalf("更新现有 Session 失败: %v", err)
	}
	if value, found, err := driver.Read("session-1"); err != nil || !found || value != `{"uid":8}` {
		t.Fatalf("更新后的 Session 错误: value=%q found=%t err=%v", value, found, err)
	}
	if err := driver.UpdateContext(context.Background(), "new-session", func(current string, found bool) (string, bool, error) {
		if found || current != "" {
			return "", false, errors.New("新 Session 不应已有数据")
		}
		return "created", false, nil
	}); err != nil {
		t.Fatalf("创建 Session 更新失败: %v", err)
	}
	if err := driver.Update("new-session", func(string, bool) (string, bool, error) {
		return "", true, nil
	}); err != nil {
		t.Fatalf("原子删除 Session 失败: %v", err)
	}
	if _, found, err := driver.Read("new-session"); err != nil || found {
		t.Fatalf("原子删除后 Session 仍存在: found=%t err=%v", found, err)
	}

	updateFailure := errors.New("business update failed")
	if err := driver.Update("session-1", func(string, bool) (string, bool, error) {
		return "", false, updateFailure
	}); !errors.Is(err, updateFailure) {
		t.Fatalf("Session 更新错误未传播: %v", err)
	}
	//lint:ignore SA1012 此处故意传入 nil，验证 Session 驱动的失败关闭边界。
	if err := driver.UpdateContext(nil, "session-1", func(string, bool) (string, bool, error) { return "", false, nil }); !errors.Is(err, cache.ErrInvalidCacheContext) {
		t.Fatalf("nil context 必须返回 ErrInvalidCacheContext: %v", err)
	}
	if err := driver.UpdateContext(context.Background(), "session-1", nil); err == nil {
		t.Fatal("nil Session 更新回调必须拒绝")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := driver.UpdateContext(canceled, "session-1", func(string, bool) (string, bool, error) { return "", false, nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消的 Session 更新必须返回 context.Canceled: %v", err)
	}

	if err := driver.Delete("session-1"); err != nil {
		t.Fatalf("删除缓存 Session 失败: %v", err)
	}
	if err := driver.Write("session-2", "value"); err != nil {
		t.Fatalf("准备清空 Session 失败: %v", err)
	}
	if err := store.Set("business-cache", "keep", time.Minute); err != nil {
		t.Fatalf("准备业务缓存失败: %v", err)
	}
	if err := driver.Clear(); err != nil {
		t.Fatalf("清空缓存 Session 失败: %v", err)
	}
	if _, found, err := driver.Read("session-2"); err != nil || found {
		t.Fatalf("Clear 后 Session 仍存在: found=%t err=%v", found, err)
	}
	if value, found, err := store.Get("business-cache"); err != nil || !found || value != "keep" {
		t.Fatalf("Session Clear 误删业务缓存: value=%#v found=%t err=%v", value, found, err)
	}
}

// TestCacheSessionDriverNamespacesAreIsolated 验证同一 cache store 中不同项目作用域
// 可以使用相同 Session ID，且清理一个项目不会影响另一个项目或业务缓存。
func TestCacheSessionDriverNamespacesAreIsolated(t *testing.T) {
	store := cache.NewCache(nil, cacheDriver.NewMemory())
	projectA := newCacheSessionDriver(store, time.Minute, "project-a")
	projectB := newCacheSessionDriver(store, time.Minute, "project-b")
	if err := store.Set("shared-id", "business", time.Minute); err != nil {
		t.Fatalf("写入同名业务缓存失败: %v", err)
	}
	if err := projectA.Write("shared-id", "A"); err != nil {
		t.Fatalf("写入项目 A Session 失败: %v", err)
	}
	if err := projectB.Write("shared-id", "B"); err != nil {
		t.Fatalf("写入项目 B Session 失败: %v", err)
	}
	if value, found, err := projectA.Read("shared-id"); err != nil || !found || value != "A" {
		t.Fatalf("读取项目 A Session 错误: value=%q found=%t err=%v", value, found, err)
	}
	if value, found, err := projectB.Read("shared-id"); err != nil || !found || value != "B" {
		t.Fatalf("读取项目 B Session 错误: value=%q found=%t err=%v", value, found, err)
	}
	if err := projectA.Clear(); err != nil {
		t.Fatalf("清理项目 A Session 失败: %v", err)
	}
	if _, found, err := projectA.Read("shared-id"); err != nil || found {
		t.Fatalf("项目 A Session 清理后仍存在: found=%t err=%v", found, err)
	}
	if value, found, err := projectB.Read("shared-id"); err != nil || !found || value != "B" {
		t.Fatalf("项目 A 清理误删项目 B: value=%q found=%t err=%v", value, found, err)
	}
	if value, found, err := store.Get("shared-id"); err != nil || !found || value != "business" {
		t.Fatalf("Session namespace 覆盖业务缓存: value=%#v found=%t err=%v", value, found, err)
	}
}

// TestCacheSessionUpdateCannotReviveAfterLeaseLoss 验证旧更新即使丢失租约，
// 也不能在较新的 Destroy 删除之后重新写回会话。
func TestCacheSessionUpdateCannotReviveAfterLeaseLoss(t *testing.T) {
	backend := &expiringCacheSessionLockDriver{Memory: cacheDriver.NewMemory(), lease: 15 * time.Millisecond}
	store := cache.NewCache(nil, backend)
	driver := &cacheSessionDriver{store: store, expire: time.Minute}
	if err := driver.Write("shared", "old"); err != nil {
		t.Fatalf("准备 Session 失败: %v", err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	staleDone := make(chan error, 1)
	go func() {
		staleDone <- driver.Update("shared", func(current string, found bool) (string, bool, error) {
			if !found || current != "old" {
				return "", false, errors.New("旧更新未读取初始 Session")
			}
			once.Do(func() { close(started) })
			<-release
			return "stale", false, nil
		})
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("旧更新未进入回调")
	}
	time.Sleep(30 * time.Millisecond)
	destroyDone := make(chan error, 1)
	go func() {
		destroyDone <- driver.Update("shared", func(string, bool) (string, bool, error) {
			return "", true, nil
		})
	}()
	time.Sleep(20 * time.Millisecond)
	close(release)
	if err := <-staleDone; err != nil && !errors.Is(err, cache.ErrCacheLockLost) {
		t.Fatalf("旧更新返回非预期错误: %v", err)
	}
	if err := <-destroyDone; err != nil && !errors.Is(err, cache.ErrCacheLockLost) {
		t.Fatalf("较新 Destroy 更新失败: %v", err)
	}
	if value, found, err := driver.Read("shared"); err != nil || found {
		t.Fatalf("旧更新复活了已删除 Session: value=%q found=%t err=%v", value, found, err)
	}
}

func newCacheBackedSessionManager(t *testing.T) *frameworkSession.Session {
	t.Helper()
	cookieFactory, err := frameworkCookie.NewCookie(nil)
	if err != nil {
		t.Fatalf("创建 Cookie 工厂失败: %v", err)
	}
	store := cache.NewCache(nil, cacheDriver.NewMemory())
	driver := newCacheSessionDriver(store, 10*time.Minute, "session-manager-test")
	manager, err := frameworkSession.NewSession(map[string]interface{}{
		"name": "CACHESESSID", "type": "cache", "expire": 600,
	}, driver, cookieFactory)
	if err != nil {
		t.Fatalf("创建 cache-backed Session 管理器失败: %v", err)
	}
	return manager
}

func persistCacheBackedSession(t *testing.T, manager *frameworkSession.Session) *http.Cookie {
	t.Helper()
	recorder := httptest.NewRecorder()
	requestSession, err := manager.NewRequestSession(httptest.NewRequest(http.MethodGet, "/", nil), recorder)
	if err != nil {
		t.Fatalf("创建初始 Session 失败: %v", err)
	}
	if err = requestSession.Set("uid", 7); err != nil {
		t.Fatalf("设置初始 Session 失败: %v", err)
	}
	if err = requestSession.Save(); err != nil {
		t.Fatalf("保存初始 Session 失败: %v", err)
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("初始 Session Cookie 数量错误: %d", len(cookies))
	}
	return cookies[0]
}

func cacheSessionRequest(cookieValue *http.Cookie) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	copyCookie := *cookieValue
	request.AddCookie(&copyCookie)
	return request
}

// TestCacheBackedSessionConcurrentSaveDestroyAndRegenerate 验证真实 Session 管理器
// 在 cache 适配器上合并并发 Save，并通过撤销墓碑阻断 Destroy/Regenerate 后的旧请求。
func TestCacheBackedSessionConcurrentSaveDestroyAndRegenerate(t *testing.T) {
	manager := newCacheBackedSessionManager(t)
	cookieValue := persistCacheBackedSession(t, manager)
	first, err := manager.NewRequestSession(cacheSessionRequest(cookieValue), httptest.NewRecorder())
	if err != nil {
		t.Fatalf("创建首个并发 Session 失败: %v", err)
	}
	second, err := manager.NewRequestSession(cacheSessionRequest(cookieValue), httptest.NewRecorder())
	if err != nil {
		t.Fatalf("创建第二个并发 Session 失败: %v", err)
	}
	if err = first.Set("first", true); err != nil {
		t.Fatalf("设置首个并发值失败: %v", err)
	}
	if err = second.Set("second", true); err != nil {
		t.Fatalf("设置第二个并发值失败: %v", err)
	}
	saveErrors := make(chan error, 2)
	go func() { saveErrors <- first.Save() }()
	go func() { saveErrors <- second.Save() }()
	for index := 0; index < 2; index++ {
		if saveErr := <-saveErrors; saveErr != nil {
			t.Fatalf("并发 Save 失败: %v", saveErr)
		}
	}
	merged, err := manager.NewRequestSession(cacheSessionRequest(cookieValue), httptest.NewRecorder())
	if err != nil {
		t.Fatalf("读取并发合并结果失败: %v", err)
	}
	if firstValue, firstFound := merged.Get("first"); !firstFound || firstValue != true {
		t.Fatalf("并发 Save 丢失 first: value=%#v found=%t", firstValue, firstFound)
	}
	if secondValue, secondFound := merged.Get("second"); !secondFound || secondValue != true {
		t.Fatalf("并发 Save 丢失 second: value=%#v found=%t", secondValue, secondFound)
	}

	fresh, err := manager.NewRequestSession(cacheSessionRequest(cookieValue), httptest.NewRecorder())
	if err != nil {
		t.Fatalf("创建 Regenerate 请求失败: %v", err)
	}
	stale, err := manager.NewRequestSession(cacheSessionRequest(cookieValue), httptest.NewRecorder())
	if err != nil {
		t.Fatalf("创建 Regenerate 旧请求失败: %v", err)
	}
	if err = fresh.Regenerate(); err != nil {
		t.Fatalf("Regenerate 失败: %v", err)
	}
	if err = fresh.Save(); err != nil {
		t.Fatalf("保存轮换后 Session 失败: %v", err)
	}
	if err = stale.Set("stale", true); err != nil {
		t.Fatalf("设置 Regenerate 旧请求失败: %v", err)
	}
	if err = stale.Save(); !errors.Is(err, frameworkSession.ErrSessionRevoked) {
		t.Fatalf("Regenerate 后旧 Save 必须被撤销墓碑拒绝: %v", err)
	}

	destroyCookie := persistCacheBackedSession(t, manager)
	destroyer, err := manager.NewRequestSession(cacheSessionRequest(destroyCookie), httptest.NewRecorder())
	if err != nil {
		t.Fatalf("创建 Destroy 请求失败: %v", err)
	}
	destroyStale, err := manager.NewRequestSession(cacheSessionRequest(destroyCookie), httptest.NewRecorder())
	if err != nil {
		t.Fatalf("创建 Destroy 旧请求失败: %v", err)
	}
	if err = destroyer.Destroy(); err != nil {
		t.Fatalf("Destroy 失败: %v", err)
	}
	if err = destroyer.Save(); err != nil {
		t.Fatalf("保存 Destroy Cookie 失败: %v", err)
	}
	if err = destroyStale.Set("revive", true); err != nil {
		t.Fatalf("设置 Destroy 旧请求失败: %v", err)
	}
	if err = destroyStale.Save(); !errors.Is(err, frameworkSession.ErrSessionRevoked) {
		t.Fatalf("Destroy 后旧 Save 必须被撤销墓碑拒绝: %v", err)
	}
}
