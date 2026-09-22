package binding

import (
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

// TestBindingUsesRequestOverrides 验证中间件覆盖值和自动绑定使用相同来源。
func TestBindingUsesRequestOverrides(t *testing.T) {
	type bodyInput struct {
		Input
		Name    string   `json:"name"`
		Page    int      `query:"page"`
		Tags    []string `query:"tags"`
		Token   string   `header:"X-Token"`
		Session string   `cookie:"session"`
	}
	raw := httptest.NewRequest("POST", "/?page=1", strings.NewReader(`{"name":"old"}`))
	raw.Header.Set("Content-Type", "application/json")
	raw.Header.Set("X-Token", "old")
	raw.Header.Set("Cookie", "session=old")
	request, err := fwcontext.NewRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	request.WithGet(map[string]any{"page": 2, "tags": []string{"a", "b"}}).
		WithPost(map[string]any{"name": "new"}).
		WithHeader(map[string]string{"Content-Type": "application/json", "X-Token": "new"}).
		WithCookie(map[string]any{"session": "new"})
	var input bodyInput
	if err := Bind(request, &input); err != nil {
		t.Fatal(err)
	}
	if input.Name != "new" || input.Page != 2 || input.Token != "new" || input.Session != "new" || !reflect.DeepEqual(input.Tags, []string{"a", "b"}) {
		t.Fatalf("绑定绕过覆盖来源: %#v", input)
	}
	type formInput struct {
		Name string `form:"name"`
	}
	raw = httptest.NewRequest("POST", "/", strings.NewReader("name=old"))
	raw.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request, err = fwcontext.NewRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	request.WithPost(map[string]any{"name": "new"})
	var form formInput
	if err := Bind(request, &form); err != nil || form.Name != "new" {
		t.Fatalf("表单绑定绕过覆盖来源: %#v %v", form, err)
	}
}

// TestBindingRetainsSourceMultiplicity 验证覆盖值同样执行重复标量和未知字段检查。
func TestBindingRetainsSourceMultiplicity(t *testing.T) {
	type queryInput struct {
		Page int `query:"page"`
	}
	request, err := fwcontext.NewRequest(httptest.NewRequest("GET", "/", nil))
	if err != nil {
		t.Fatal(err)
	}
	request.WithGet(map[string]any{"page": []string{"1", "2"}})
	if err := Bind(request, &queryInput{}); err == nil {
		t.Fatal("重复覆盖参数被静默折叠")
	}
	request = bindingRequest(t, `{}`).WithPost(map[string]any{"unknown": "value"})
	if err := Bind(request, &struct {
		Name string `json:"name"`
	}{}); err == nil {
		t.Fatal("覆盖请求体绕过未知字段检查")
	}
}

// TestBindingSeesReplacedInput 验证已解析请求仍可被中间件替换，空对象和非法 JSON 都不能沿用旧值。
func TestBindingSeesReplacedInput(t *testing.T) {
	request := bindingRequest(t, `{"name":"old"}`)
	var input struct {
		Name string `json:"name"`
	}
	if err := Bind(request, &input); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"name":"new"}`, `{}`, `{"name":"a","name":"b"}`, `null`} {
		request.WithInput(body)
		err := Bind(request, &input)
		switch body {
		case `{"name":"new"}`:
			if err != nil || input.Name != "new" {
				t.Fatalf("新输入未生效: %#v %v", input, err)
			}
		case `{}`:
			if err != nil || input.Name != "" {
				t.Fatalf("空对象未清除旧值: %#v %v", input, err)
			}
		default:
			if err == nil {
				t.Fatalf("非法覆盖输入未拒绝: %s", body)
			}
		}
	}
}

// BenchmarkBindScalarObject 衡量重复绑定同一已解析请求的转换成本，避免混入网络和请求创建开销。
func BenchmarkBindScalarObject(b *testing.B) {
	raw := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"Ada","age":31,"active":true,"score":12.5,"token":"YWRh"}`))
	raw.Header.Set("Content-Type", "application/json")
	request, err := fwcontext.NewRequest(raw)
	if err != nil {
		b.Fatal(err)
	}
	var input struct {
		Name   string  `json:"name"`
		Age    int     `json:"age"`
		Active bool    `json:"active"`
		Score  float64 `json:"score"`
		Token  []byte  `json:"token"`
	}
	if err := Bind(request, &input); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if err := Bind(request, &input); err != nil {
			b.Fatal(err)
		}
	}
}
