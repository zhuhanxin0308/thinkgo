//go:build integration

package context

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	frameworkcookie "github.com/zhuhanxin0308/thinkgo/v3/cookie"
	frameworksession "github.com/zhuhanxin0308/thinkgo/v3/session"
	sessiondriver "github.com/zhuhanxin0308/thinkgo/v3/session/driver"
)

// TestLiveRedisTokenRejectsConcurrentReplay 验证真实 Redis 上并发请求只能消费一次令牌。
func TestLiveRedisTokenRejectsConcurrentReplay(t *testing.T) {
	host, hostOK := os.LookupEnv("THINKGO_LIVE_REDIS_HOST")
	portText, portOK := os.LookupEnv("THINKGO_LIVE_REDIS_PORT")
	if !hostOK || !portOK {
		t.Skip("真实 Redis 地址未配置")
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("thinkgo:v301:token:%d:%d:", os.Getpid(), time.Now().UnixNano())
	driver, err := sessiondriver.NewRedis(map[string]interface{}{"host": host, "port": port, "prefix": prefix, "timeout_ms": 1000})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := driver.Clear(); err != nil {
			t.Error(err)
		}
		if err := driver.Close(); err != nil {
			t.Error(err)
		}
	})
	cookieFactory, err := frameworkcookie.NewCookie(map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := frameworksession.NewSession(map[string]interface{}{"name": "THINKGOSESSID", "expire": 60}, driver, cookieFactory)
	if err != nil {
		t.Fatal(err)
	}
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
	requests := []*Request{makeRequest(), makeRequest()}
	var wait sync.WaitGroup
	accepted := make([]bool, len(requests))
	for index, request := range requests {
		wait.Add(1)
		go func() {
			defer wait.Done()
			accepted[index] = request.CheckToken()
		}()
	}
	wait.Wait()
	if accepted[0] == accepted[1] || requests[0].TokenError() != nil || requests[1].TokenError() != nil {
		t.Fatalf("真实 Redis 应仅接受一个请求: accepted=%v errors=[%v %v]", accepted, requests[0].TokenError(), requests[1].TokenError())
	}
}
