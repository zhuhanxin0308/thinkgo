package http

import "testing"

// TestReservedControllerMethods 验证基类内置方法被识别为保留方法，
// 自动路由不会把它们暴露为可达端点；用户自定义动作名不受影响。
func TestReservedControllerMethods(t *testing.T) {
	reserved := []string{"View", "Fetch", "Success", "Error", "Result", "Redirect", "Validate", "Init", "SetMiddleware", "GetMiddleware", "Assign"}
	for _, name := range reserved {
		if !isReservedControllerMethod(name) {
			t.Fatalf("基类方法 %q 应被识别为保留方法", name)
		}
	}

	allowed := []string{"Index", "Detail", "Profile", "List"}
	for _, name := range allowed {
		if isReservedControllerMethod(name) {
			t.Fatalf("用户自定义动作 %q 不应被判定为保留方法", name)
		}
	}
}
