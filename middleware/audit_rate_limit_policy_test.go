package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/ratelimit"
)

// TestAuditRateLimitSharedStoreMustNotResetStrictQuota exercises the actual
// middleware and MemoryStore. Different policies must either be isolated or
// rejected; switching endpoints must not replenish the strict quota.
func TestAuditRateLimitSharedStoreMustNotResetStrictQuota(t *testing.T) {
	store, err := ratelimit.NewMemoryStore(16)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	strict, err := NewRateLimit(RateLimitConfig{
		Store: store, Limit: ratelimit.Limit{Rate: 1, Period: time.Minute, Burst: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	loose, err := NewRateLimit(RateLimitConfig{
		Store: store, Limit: ratelimit.Limit{Rate: 100, Period: time.Minute, Burst: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	fixed := time.Unix(1700000000, 0)
	strict.clock = func() time.Time { return fixed }
	loose.clock = func() time.Time { return fixed }
	request := func(limiter *RateLimit, path string) int {
		t.Helper()
		raw := httptest.NewRequest(http.MethodGet, "http://example.test"+path, nil)
		raw.RemoteAddr = "192.0.2.8:50000"
		req, err := fwcontext.NewRequest(raw)
		if err != nil {
			t.Fatal(err)
		}
		response := limiter.Handle(req, func(*fwcontext.Request) *fwcontext.Response {
			return fwcontext.NewResponse().Content("ok")
		})
		if response == nil {
			t.Fatal("nil response")
		}
		return response.GetStatus()
	}
	if code := request(strict, "/strict"); code != http.StatusOK {
		t.Fatalf("first request: got=%d want=200", code)
	}
	if code := request(strict, "/strict"); code != http.StatusTooManyRequests {
		t.Fatalf("same-policy control: got=%d want=429", code)
	}
	if code := request(loose, "/loose"); code != http.StatusOK {
		t.Fatalf("independent policy: got=%d want=200", code)
	}
	if code := request(strict, "/strict"); code != http.StatusTooManyRequests {
		t.Fatalf("strict quota changed after switching policies: got=%d want=429", code)
	}
	peer, err := NewRateLimit(RateLimitConfig{
		Store: store, Limit: ratelimit.Limit{Rate: 1, Period: time.Minute, Burst: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	peer.clock = strict.clock
	if code := request(peer, "/strict"); code != http.StatusTooManyRequests {
		t.Fatalf("same policy must share quota across instances: got=%d want=429", code)
	}
}
