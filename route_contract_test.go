package framework

import (
	"errors"
	"net/http"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework/route"
)

// TestRouteFacadeContractPreservesScope 验证现有应用路由门面可以原子注册契约并保留分组前缀。
func TestRouteFacadeContractPreservesScope(t *testing.T) {
	router := route.NewRouter()
	facade := newRouteFacade(nil, router)
	commit := func(info route.RouteInfo) error {
		if info.Path != "/api/users" {
			t.Fatalf("门面丢失分组: %s", info.Path)
		}
		return nil
	}
	facade.Group("api", func() {
		if _, err := facade.AddWithCommit(http.MethodGet, "/users", func() {}, commit); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := facade.AddWithCommit(http.MethodGet, "/plain", func() {}, func(route.RouteInfo) error { return nil }); err != nil {
		t.Fatal(err)
	}
	var absent *Route
	if _, err := absent.AddWithCommit(http.MethodGet, "/plain", func() {}, commit); !errors.Is(err, route.ErrInvalidRoute) {
		t.Fatalf("空门面错误: %v", err)
	}
}
