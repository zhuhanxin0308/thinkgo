package route

import (
	"reflect"
	"testing"
)

// TestRouteParameterNamesPreserveDeclarationOrder 验证动作参数绑定使用路由定义顺序，
// 而不是依赖 map 的随机遍历顺序。
func TestRouteParameterNamesPreserveDeclarationOrder(t *testing.T) {
	router := NewRouter()
	registered, err := router.Get("/users/:user/orders/:order?", "order/show")
	if err != nil {
		t.Fatalf("注册参数顺序测试路由失败: %v", err)
	}
	if names := registered.ParameterNames(); !reflect.DeepEqual(names, []string{"user", "order"}) {
		t.Fatalf("路由参数顺序错误: %#v", names)
	}
	names := registered.ParameterNames()
	names[0] = "changed"
	if current := registered.ParameterNames(); current[0] != "user" {
		t.Fatalf("调用方不得修改路由内部参数顺序: %#v", current)
	}
}
