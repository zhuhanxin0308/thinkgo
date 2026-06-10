package route

import (
	"reflect"
	"testing"
)

// TestDynamicRoutesIndexedByMethod 验证动态路由会按 HTTP 方法单独建立索引，避免匹配时扫描全部路由。
func TestDynamicRoutesIndexedByMethod(t *testing.T) {
	router := NewRouter()
	router.Get("/api/users/:id", "User@Show")
	router.Post("/api/users/:id", "User@Update")
	router.Any("/api/common/:name", "Common@Handle")

	dynamicField := reflect.ValueOf(router).Elem().FieldByName("dynamicRoutes")
	if !dynamicField.IsValid() {
		t.Fatal("Router 应维护动态路由方法索引")
	}

	getRoutes := dynamicField.MapIndex(reflect.ValueOf("GET"))
	if !getRoutes.IsValid() || getRoutes.Len() != 1 {
		t.Fatalf("GET 动态路由索引数量应为 1，实际为 %d", getRoutes.Len())
	}

	postRoutes := dynamicField.MapIndex(reflect.ValueOf("POST"))
	if !postRoutes.IsValid() || postRoutes.Len() != 1 {
		t.Fatalf("POST 动态路由索引数量应为 1，实际为 %d", postRoutes.Len())
	}

	anyRoutes := dynamicField.MapIndex(reflect.ValueOf("*"))
	if !anyRoutes.IsValid() || anyRoutes.Len() != 1 {
		t.Fatalf("ANY 动态路由索引数量应为 1，实际为 %d", anyRoutes.Len())
	}
}
