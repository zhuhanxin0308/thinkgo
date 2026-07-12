package route

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	fwcontext "thinkgo/framework/context"
)

// TestDynamicRoutesIndexedByMethod 验证冻结后只扫描当前方法和通配方法的动态路由。
func TestDynamicRoutesIndexedByMethod(t *testing.T) {
	router := NewRouter()
	if _, err := router.Get("/api/users/:id", "User@Show"); err != nil {
		t.Fatalf("注册 GET 动态路由失败: %v", err)
	}
	if _, err := router.Post("/api/users/:id", "User@Update"); err != nil {
		t.Fatalf("注册 POST 动态路由失败: %v", err)
	}
	if _, err := router.Any("/api/common/:name", "Common@Handle"); err != nil {
		t.Fatalf("注册 ANY 动态路由失败: %v", err)
	}
	if err := router.Freeze(); err != nil {
		t.Fatalf("冻结路由失败: %v", err)
	}

	if len(router.dynamicRoutes[http.MethodGet]) != 1 {
		t.Fatalf("GET 动态路由索引数量应为 1，实际为 %d", len(router.dynamicRoutes[http.MethodGet]))
	}
	if len(router.dynamicRoutes[http.MethodPost]) != 1 {
		t.Fatalf("POST 动态路由索引数量应为 1，实际为 %d", len(router.dynamicRoutes[http.MethodPost]))
	}
	if len(router.dynamicRoutes[anyMethod]) != 1 {
		t.Fatalf("ANY 动态路由索引数量应为 1，实际为 %d", len(router.dynamicRoutes[anyMethod]))
	}
}

// TestRouterConcurrentRegistrationAndMatching 验证无共享分组状态时并发注册安全，冻结后可并发只读匹配。
func TestRouterConcurrentRegistrationAndMatching(t *testing.T) {
	router := NewRouter()
	const routeCount = 64
	errorsChannel := make(chan error, routeCount)
	var registrations sync.WaitGroup
	for index := 0; index < routeCount; index++ {
		index := index
		registrations.Add(1)
		go func() {
			defer registrations.Done()
			_, err := router.Get(fmt.Sprintf("/items/%d", index), "Item@Show")
			errorsChannel <- err
		}()
	}
	registrations.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatalf("并发注册路由失败: %v", err)
		}
	}
	if err := router.Freeze(); err != nil {
		t.Fatalf("冻结并发注册结果失败: %v", err)
	}

	var matches sync.WaitGroup
	for index := 0; index < routeCount; index++ {
		index := index
		matches.Add(1)
		go func() {
			defer matches.Done()
			req := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, fmt.Sprintf("http://example.com/items/%d", index), nil))
			matched, _, err := router.Match(req)
			if err != nil || matched == nil {
				t.Errorf("并发匹配路由 %d 失败，路由=%#v 错误=%v", index, matched, err)
			}
		}()
	}
	matches.Wait()
}
