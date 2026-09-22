package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	fwcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
	"github.com/zhuhanxin0308/thinkgo/framework/ratelimit"
)

type failingRateLimitStore struct {
	err error
}

func (store failingRateLimitStore) Take(context.Context, string, ratelimit.Limit, time.Time) (ratelimit.Result, error) {
	if store.err != nil {
		return ratelimit.Result{}, store.err
	}
	return ratelimit.Result{}, errors.New("存储不可用")
}

// TestRateLimitUsesTrustedClientIPAndReturnsHeaders 验证客户端键复用可信代理解析并返回额度响应头。
func TestRateLimitUsesTrustedClientIPAndReturnsHeaders(t *testing.T) {
	store, _ := ratelimit.NewMemoryStore(10)
	handler, err := NewRateLimit(RateLimitConfig{
		Store: store,
		Limit: ratelimit.Limit{Rate: 1, Period: time.Second, Burst: 1},
	})
	if err != nil {
		t.Fatalf("创建限流中间件失败: %v", err)
	}
	now := time.Unix(300, 0)
	handler.clock = func() time.Time { return now }
	request := newRateLimitRequest(t, "10.0.0.2:1234", "198.51.100.8, 10.0.0.2")
	first := handler.Handle(request, func(*fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Content("ok")
	})
	if first.GetStatus() != http.StatusOK || first.Headers().Get("RateLimit-Limit") != "1" || first.Headers().Get("RateLimit-Remaining") != "0" {
		t.Fatalf("首次请求响应错误: status=%d headers=%v", first.GetStatus(), first.Headers())
	}
	second := handler.Handle(newRateLimitRequest(t, "10.0.0.2:1234", "198.51.100.8, 10.0.0.2"), func(*fwcontext.Request) *fwcontext.Response {
		t.Fatal("超额请求不应进入下游")
		return nil
	})
	if second.GetStatus() != http.StatusTooManyRequests || second.Headers().Get("Retry-After") != "1" || second.Headers().Get("Cache-Control") != "no-store" {
		t.Fatalf("超额响应错误: status=%d headers=%v", second.GetStatus(), second.Headers())
	}
}

// TestRateLimitFailsClosedOnInvalidKeyAndSeparatesStoreFailure 验证非法键失败关闭，后端故障返回 503。
func TestRateLimitFailsClosedOnInvalidKeyAndSeparatesStoreFailure(t *testing.T) {
	store, _ := ratelimit.NewMemoryStore(10)
	invalidKey, err := NewRateLimit(RateLimitConfig{
		Store: store,
		Limit: ratelimit.Limit{Rate: 1, Period: time.Second, Burst: 1},
		Key:   func(*fwcontext.Request) (string, error) { return "", nil },
	})
	if err != nil {
		t.Fatalf("创建自定义键限流器失败: %v", err)
	}
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "/", nil))
	if response := invalidKey.Handle(request, func(*fwcontext.Request) *fwcontext.Response { return nil }); response.GetStatus() != http.StatusBadRequest {
		t.Fatalf("非法键应返回 400，实际为 %d", response.GetStatus())
	}

	failing, err := NewRateLimit(RateLimitConfig{
		Store: failingRateLimitStore{},
		Limit: ratelimit.Limit{Rate: 1, Period: time.Second, Burst: 1},
	})
	if err != nil {
		t.Fatalf("创建故障存储限流器失败: %v", err)
	}
	if response := failing.Handle(request, func(*fwcontext.Request) *fwcontext.Response { return nil }); response.GetStatus() != http.StatusServiceUnavailable {
		t.Fatalf("存储故障应返回 503，实际为 %d", response.GetStatus())
	}

	capacity, err := NewRateLimit(RateLimitConfig{
		Store: failingRateLimitStore{err: ratelimit.ErrStoreCapacity},
		Limit: ratelimit.Limit{Rate: 1, Period: time.Second, Burst: 1},
	})
	if err != nil {
		t.Fatalf("创建容量耗尽限流器失败: %v", err)
	}
	capacityResponse := capacity.Handle(request, func(*fwcontext.Request) *fwcontext.Response { return nil })
	if capacityResponse.GetStatus() != http.StatusServiceUnavailable || capacityResponse.GetHeader("Retry-After") != "1" {
		t.Fatalf("存储容量耗尽属于服务故障，应返回 503 和重试提示: status=%d headers=%v", capacityResponse.GetStatus(), capacityResponse.Headers())
	}
}

func newRateLimitRequest(t *testing.T, remoteAddr, forwardedFor string) *fwcontext.Request {
	t.Helper()
	raw := httptest.NewRequest(http.MethodGet, "/", nil)
	raw.RemoteAddr = remoteAddr
	raw.Header.Set("X-Forwarded-For", forwardedFor)
	request, err := fwcontext.NewRequest(raw, fwcontext.WithTrustedProxies([]string{"10.0.0.0/8"}))
	if err != nil {
		t.Fatalf("创建限流请求失败: %v", err)
	}
	return request
}
