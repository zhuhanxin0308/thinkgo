package session

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/cookie"
)

// TestSessionReadSnapshotsDuringMutation 同时变更原始状态和读取者的私有结果，检查借用原文的生命周期。
func TestSessionReadSnapshotsDuringMutation(t *testing.T) {
	manager := newTestSessionManager(t, newCountingDriver(), nil, nil)
	request, err := manager.NewRequestSession(httptest.NewRequest(http.MethodGet, "http://example.com/", nil), httptest.NewRecorder())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for worker := 0; worker < 5; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			<-start
			for i := 0; i < 100; i++ {
				if worker == 0 {
					if err := request.Set("value", map[string]interface{}{"n": i, "items": []int{i, i}}); err != nil {
						t.Error(err)
						return
					}
					if i%3 == 0 {
						if err := request.Save(); err != nil {
							t.Error(err)
							return
						}
					}
					if i%5 == 0 {
						if err := request.Delete("value"); err != nil {
							t.Error(err)
							return
						}
					}
					if i%7 == 0 {
						if err := request.Clear(); err != nil {
							t.Error(err)
							return
						}
					}
					continue
				}
				var value interface{}
				var found bool
				if worker%2 == 0 {
					all := request.All()
					value, found = all["value"]
					all["private"] = "changed"
				} else {
					value, found = request.Get("value")
				}
				if !found {
					continue
				}
				fields := value.(map[string]interface{})
				items := fields["items"].([]interface{})
				if len(items) != 2 || items[0] != fields["n"] || items[1] != fields["n"] {
					t.Error("读到了不完整的 JSON 快照")
					return
				}
				fields["n"] = "private"
				items[0] = "private"
			}
		}(worker)
	}
	close(start)
	wg.Wait()
}

func BenchmarkSessionSnapshotRead(b *testing.B) {
	for _, size := range []int{64, 1024, 16384} {
		for _, method := range []string{"Get", "All"} {
			b.Run(fmt.Sprintf("%s_%d", method, size), func(b *testing.B) {
				cookies, err := cookie.NewCookie(nil)
				if err != nil {
					b.Fatal(err)
				}
				manager, err := NewSession(nil, newCountingDriver(), cookies)
				if err != nil {
					b.Fatal(err)
				}
				request, err := manager.NewRequestSession(httptest.NewRequest(http.MethodGet, "http://example.com/", nil), httptest.NewRecorder())
				if err != nil {
					b.Fatal(err)
				}
				if err := request.Set("value", map[string]interface{}{"body": strings.Repeat("x", size), "count": 42}); err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					if method == "Get" {
						if _, ok := request.Get("value"); !ok {
							b.Fatal("缺少 value")
						}
					} else {
						if len(request.All()) != 1 {
							b.Fatal("快照不完整")
						}
					}
				}
			})
		}
	}
}
