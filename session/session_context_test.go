package session

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

type sessionContextKey struct{}

type contextTrackingSessionDriver struct {
	*countingDriver
	mu            sync.Mutex
	readContext   context.Context
	updateContext context.Context
}

func (driver *contextTrackingSessionDriver) ReadContext(ctx context.Context, id string) (string, bool, error) {
	driver.mu.Lock()
	driver.readContext = ctx
	driver.mu.Unlock()
	return driver.countingDriver.Read(id)
}

func (driver *contextTrackingSessionDriver) UpdateContext(ctx context.Context, id string, update func(string, bool) (string, bool, error)) error {
	driver.mu.Lock()
	driver.updateContext = ctx
	driver.mu.Unlock()
	return driver.countingDriver.Update(id, update)
}

func (driver *contextTrackingSessionDriver) contexts() (context.Context, context.Context) {
	driver.mu.Lock()
	defer driver.mu.Unlock()
	return driver.readContext, driver.updateContext
}

// TestRequestSessionPropagatesDriverContext 验证请求上下文会传递给可选的 Session Driver 接口。
func TestRequestSessionPropagatesDriverContext(t *testing.T) {
	driver := &contextTrackingSessionDriver{countingDriver: newCountingDriver()}
	manager := newTestSessionManager(t, driver, map[string]interface{}{"name": "SID", "type": "memory"}, nil)

	firstContext := context.WithValue(context.Background(), sessionContextKey{}, "first")
	firstRequest := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(firstContext)
	firstWriter := httptest.NewRecorder()
	firstSession, err := manager.NewRequestSession(firstRequest, firstWriter)
	if err != nil {
		t.Fatalf("创建首个请求 Session 失败: %v", err)
	}
	if err := firstSession.Set("answer", "ok"); err != nil {
		t.Fatalf("设置首个请求 Session 失败: %v", err)
	}
	if err := firstSession.Save(); err != nil {
		t.Fatalf("保存首个请求 Session 失败: %v", err)
	}
	_, updateContext := driver.contexts()
	if updateContext == nil || updateContext.Value(sessionContextKey{}) != "first" {
		t.Fatal("Session UpdateContext 未收到原始请求上下文")
	}

	secondContext := context.WithValue(context.Background(), sessionContextKey{}, "second")
	secondRequest := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(secondContext)
	for _, cookie := range firstWriter.Result().Cookies() {
		secondRequest.AddCookie(cookie)
	}
	secondSession, err := manager.NewRequestSession(secondRequest, httptest.NewRecorder())
	if err != nil {
		t.Fatalf("创建第二个请求 Session 失败: %v", err)
	}
	if value, found := secondSession.Get("answer"); !found || value != "ok" {
		t.Fatalf("第二个请求未读取首个 Session 数据: value=%#v found=%t", value, found)
	}
	readContext, _ := driver.contexts()
	if readContext == nil || readContext.Value(sessionContextKey{}) != "second" {
		t.Fatal("Session ReadContext 未收到原始请求上下文")
	}
}
