package context

import (
	"net/http"
	"net/http/httptest"
	"testing"

	frameworkcookie "github.com/zhuhanxin0308/thinkgo/v3/cookie"
	frameworksession "github.com/zhuhanxin0308/thinkgo/v3/session"
	sessiondriver "github.com/zhuhanxin0308/thinkgo/v3/session/driver"
)

// TestBuildTokenUsesFreshRandomValue 验证同一请求连续生成的令牌互不相同。
func TestBuildTokenUsesFreshRandomValue(t *testing.T) {
	cookieFactory, err := frameworkcookie.NewCookie(map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := frameworksession.NewSession(map[string]interface{}{"name": "THINKGOSESSID"}, sessiondriver.NewMemory(), cookieFactory)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	raw := httptest.NewRequest(http.MethodPost, "http://example.com/form", nil)
	current, err := manager.NewRequestSession(raw, httptest.NewRecorder())
	if err != nil {
		t.Fatal(err)
	}
	request := newRequestForTest(t, raw).WithSession(current)
	first, second := request.BuildToken(), request.BuildToken()
	if first == "" || second == "" || first == second {
		t.Fatalf("连续生成的令牌必须是独立随机值: first=%q second=%q err=%v", first, second, request.TokenError())
	}
}

// TestCheckTokenRejectsOverlappingReplay 验证独立请求加载同一旧快照后只有一次消费成功。
func TestCheckTokenRejectsOverlappingReplay(t *testing.T) {
	cookieFactory, err := frameworkcookie.NewCookie(map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := frameworksession.NewSession(map[string]interface{}{"name": "THINKGOSESSID"}, sessiondriver.NewMemory(), cookieFactory)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	initialRaw := httptest.NewRequest(http.MethodPost, "http://example.com/form", nil)
	initialWriter := httptest.NewRecorder()
	initialSession, err := manager.NewRequestSession(initialRaw, initialWriter)
	if err != nil {
		t.Fatal(err)
	}
	token := newRequestForTest(t, initialRaw).WithSession(initialSession).BuildToken()
	if token == "" {
		t.Fatal("初始令牌为空")
	}
	if err := initialSession.Save(); err != nil {
		t.Fatal(err)
	}
	makeRequest := func() *Request {
		raw := httptest.NewRequest(http.MethodPost, "http://example.com/form", nil)
		for _, current := range initialWriter.Result().Cookies() {
			raw.AddCookie(current)
		}
		current, sessionErr := manager.NewRequestSession(raw, httptest.NewRecorder())
		if sessionErr != nil {
			t.Fatal(sessionErr)
		}
		return newRequestForTest(t, raw).WithSession(current).WithPost(map[string]interface{}{"__token__": token})
	}
	first, second := makeRequest(), makeRequest()
	if !first.CheckToken() {
		t.Fatalf("首次消费失败: %v", first.TokenError())
	}
	if second.CheckToken() {
		t.Fatal("旧快照中的同一令牌被第二次接受")
	}
}
