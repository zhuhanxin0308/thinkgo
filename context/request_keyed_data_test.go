package context

import "testing"

func TestRequestValueKeysIsolateSameTypeAndRequest(t *testing.T) {
	first, second := new(RequestValueKey[any]), new(RequestValueKey[any])
	var request, other Request
	request.Set("identity", "public")
	first.Store(&request, nil)
	second.Store(&request, "second")
	first.Store(&other, "other")
	if value, exists := first.Load(&request); value != nil || !exists {
		t.Fatalf("已设置的 nil 必须与缺少值区分，实际为 %v, %t", value, exists)
	}
	if value, exists := second.Load(&request); value != "second" || !exists {
		t.Fatalf("同类型的另一个键应有自己的值，实际为 %v, %t", value, exists)
	}
	if value, exists := first.Load(&other); value != "other" || !exists {
		t.Fatalf("另一请求应有自己的值，实际为 %v, %t", value, exists)
	}
	if value, exists := second.Load(&other); value != nil || exists {
		t.Fatalf("缺少值时应返回零值和 false，实际为 %v, %t", value, exists)
	}
	snapshot := request.Middleware().(map[string]interface{})
	if len(snapshot) != 1 || snapshot["identity"] != "public" {
		t.Fatalf("普通中间件快照应仅包含字符串键数据，实际为 %#v", snapshot)
	}
	first.Store(nil, "ignored")
	if _, exists := first.Load(nil); exists {
		t.Fatal("空请求不应返回值")
	}
	var missing *RequestValueKey[any]
	missing.Store(&request, "ignored")
	if _, exists := missing.Load(&request); exists {
		t.Fatal("空键不应返回值")
	}
}
