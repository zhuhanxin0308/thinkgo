package context

import (
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestRequestConcurrentLazyCaches 并发访问 body/json/query/form 惰性缓存，
// 配合 `go test -race` 可检出之前未加锁导致的数据竞争。
func TestRequestConcurrentLazyCaches(t *testing.T) {
	body := `{"name":"alice","age":30}`
	raw := httptest.NewRequest("POST", "/users?page=2", strings.NewReader(body))
	raw.Header.Set("Content-Type", "application/json")
	req := NewRequest(raw)

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = req.All()
			_, _ = req.Body()
			_ = req.Param("name")
			_ = req.Get("page")
			_ = req.Post("name")
		}()
	}
	wg.Wait()

	if got := req.Param("name"); got != "alice" {
		t.Fatalf("并发后参数读取应为 alice，得到 %q", got)
	}
	if got := req.Get("page"); got != "2" {
		t.Fatalf("并发后查询参数应为 2，得到 %q", got)
	}
}

// TestUrlencodedBodyPreservedAfterFormParse 验证 urlencoded 表单解析后请求体仍可读，
// 修复了 ParseForm 消费 body 导致 Body()/Json() 读空的顺序问题。
func TestUrlencodedBodyPreservedAfterFormParse(t *testing.T) {
	form := "name=bob&city=paris"
	raw := httptest.NewRequest("POST", "/submit", strings.NewReader(form))
	raw.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req := NewRequest(raw)

	// 先触发表单解析
	if got := req.Post("name"); got != "bob" {
		t.Fatalf("表单字段 name 应为 bob，得到 %q", got)
	}

	// 表单解析后原始请求体仍应完整可读
	bodyBytes, err := req.Body()
	if err != nil {
		t.Fatalf("读取请求体出错: %v", err)
	}
	if string(bodyBytes) != form {
		t.Fatalf("表单解析后请求体应保持完整 %q，得到 %q", form, string(bodyBytes))
	}
}
