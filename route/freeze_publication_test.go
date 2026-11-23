package route

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

// 并发首请求只能看到完整索引或同一冻结错误；后续匹配独立于命名缓存的写锁。
func TestRouterPublishesFreezeResultToMatchers(t *testing.T) {
	const (
		readers               = 16
		matchDuringWriteLimit = 2 * time.Second
	)
	for _, tc := range []struct {
		name   string
		broken bool
	}{
		{"成功", false},
		{"失败", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := NewRouter()
			registered, err := router.Get("/items/:id", "Item@Show")
			if err != nil {
				t.Fatal(err)
			}
			if tc.broken {
				// 使用与已有冻结失败回归相同的损坏状态，公开注册入口会拒绝该路径。
				registered.path = "/items//broken"
			}
			type result struct {
				route *Route
				id    string
				err   error
			}
			results := make(chan result, readers)
			start := make(chan struct{})
			match := func(req *fwcontext.Request) {
				<-start
				matched, params, err := router.Match(req)
				results <- result{matched, params["id"], err}
			}
			newRequest := func() *fwcontext.Request {
				return fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "/items/42", nil))
			}
			for range readers {
				go match(newRequest())
			}
			close(start)
			first := <-results
			for range readers - 1 {
				if got := <-results; got != first {
					t.Fatalf("并发匹配观察到不同的冻结结果：首次=%v 当前=%v", first, got)
				}
			}
			if !tc.broken && (first.route != registered || first.id != "42" || first.err != nil) {
				t.Fatalf("动态索引发布不完整：%v", first)
			}
			if tc.broken && (first.route != nil || !errors.Is(first.err, ErrInvalidRoute)) {
				t.Fatalf("损坏的路由未返回冻结错误：%v", first)
			}
			if _, err := router.Get("/late", "Late@Show"); !errors.Is(err, ErrRouterFrozen) {
				t.Fatalf("冻结后仍可注册路由：%v", err)
			}

			// 固定 LoadNameCache 发布新命名缓存时的写锁区间；Match 不读取这份缓存。
			router.mu.Lock()
			go match(newRequest())
			select {
			case got := <-results:
				router.mu.Unlock()
				if got != first {
					t.Fatalf("命名缓存写入期间冻结结果改变：首次=%v 当前=%v", first, got)
				}
			case <-time.After(matchDuringWriteLimit):
				router.mu.Unlock()
				<-results
				t.Fatal("已冻结的匹配被命名缓存写锁阻塞")
			}
		})
	}
}
