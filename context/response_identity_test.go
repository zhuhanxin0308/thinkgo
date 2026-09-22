package context

import "testing"

// TestResponseIdentitySurvivesValueCopy 验证框架响应和零值响应都会获得稳定身份，
// 并且复制 Response 值不会切断宿主保存的请求生命周期状态。
func TestResponseIdentitySurvivesValueCopy(t *testing.T) {
	responses := []*Response{NewResponse(), &Response{}}
	for index, response := range responses {
		identity := response.Identity()
		if identity == nil || response.Identity() != identity {
			t.Fatalf("第 %d 个响应未返回稳定身份", index+1)
		}
		copied := *response
		if copied.Identity() != identity {
			t.Fatalf("第 %d 个响应复制后身份发生变化", index+1)
		}
	}

	var nilResponse *Response
	if nilResponse.Identity() != nil {
		t.Fatal("空响应不应创建身份")
	}
}
