package binding

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/exception"
)

type createInput struct {
	ID      int64          `path:"id" validate:"required|gt:0"`
	Page    int            `query:"page" default:"1" validate:"between:1,100"`
	Token   string         `header:"X-Token" validate:"required"`
	Name    string         `json:"name" validate:"required|length:2,32"`
	Enabled Optional[bool] `json:"enabled"`
	Note    *string        `json:"note"`
	Items   []itemInput    `json:"items" validate:"required|length:1,5"`
}

type itemInput struct {
	Quantity int    `json:"quantity" validate:"required|between:1,10"`
	Label    string `json:"label" validate:"required"`
}

func bindingRequest(t *testing.T, body string) *fwcontext.Request {
	t.Helper()
	raw := httptest.NewRequest(http.MethodPost, "/users?page=2", strings.NewReader(body))
	raw.Header.Set("Content-Type", "application/json")
	raw.Header.Set("X-Token", "test-token")
	req := fwcontext.MustNewRequest(raw)
	req.SetRoute("id", "42")
	return req
}

// TestBindCombinesDeclaredSources 验证输入来源、嵌套数组与显式 false 均保留业务语义。
func TestBindCombinesDeclaredSources(t *testing.T) {
	req := bindingRequest(t, `{"name":"Ada","enabled":false,"note":null,"items":[{"quantity":2,"label":"first"}]}`)
	var input createInput
	if err := Bind(req, &input); err != nil {
		t.Fatal(err)
	}
	if input.ID != 42 || input.Page != 2 || input.Token != "test-token" || input.Name != "Ada" || input.Note != nil || input.Items[0].Quantity != 2 {
		t.Fatalf("输入字段绑定错误: %#v", input)
	}
	if !input.Enabled.IsSet() || input.Enabled.IsNull() || input.Enabled.Value() {
		t.Fatal("显式 false 丢失")
	}
}

// TestBindFailureIsAtomic 验证失败不能把部分用户输入发布到调用方对象。
func TestBindFailureIsAtomic(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
		field      string
	}{
		{"unknown", `{"name":"Ada","admin":true,"items":[]}`, http.StatusBadRequest, "body.admin"},
		{"duplicate", `{"name":"Ada","name":"Bob"}`, http.StatusBadRequest, "body"},
		{"type", `{"name":123}`, http.StatusBadRequest, "body.name"},
		{"null", `{"name":null}`, http.StatusBadRequest, "body.name"},
		{"required", `{"items":[{"quantity":2,"label":"x"}]}`, http.StatusUnprocessableEntity, "body.name"},
		{"nested", `{"name":"Ada","items":[{"quantity":0,"label":"x"}]}`, http.StatusUnprocessableEntity, "body.items[0].quantity"},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := createInput{Name: "unchanged", Items: []itemInput{{Quantity: 8, Label: "before"}}}
			input := original
			err := Bind(bindingRequest(t, test.body), &input)
			var failure *exception.HttpException
			if !errors.As(err, &failure) || failure.StatusCode != test.status {
				t.Fatalf("错误分类: %v", err)
			}
			issues, ok := failure.Data["errors"].([]Issue)
			found := false
			for _, issue := range issues {
				if issue.Field == test.field {
					found = true
				}
			}
			if !ok || !found {
				t.Fatalf("字段错误缺失 %s: %#v", test.field, failure.Data)
			}
			if !reflect.DeepEqual(input, original) {
				t.Fatalf("失败绑定修改了目标: %#v", input)
			}
		})
	}
}

// TestOptionalDistinguishesMissingNullAndZero 验证 PATCH 三态语义和缺失字段默认值。
func TestOptionalDistinguishesMissingNullAndZero(t *testing.T) {
	for _, body := range []string{`{}`, `{"value":null}`, `{"value":0}`} {
		var target struct {
			Value Optional[int] `json:"value"`
			Page  int           `query:"limit" default:"20"`
		}
		if err := Bind(bindingRequest(t, body), &target); err != nil {
			t.Fatal(err)
		}
		if target.Page != 20 || target.Value.IsSet() != (body != `{}`) || target.Value.IsNull() != strings.Contains(body, "null") || target.Value.Value() != 0 {
			t.Fatalf("三态绑定错误: %#v", target)
		}
	}
}

// TestBindRejectsAmbiguousParameterValues 验证重复标量拒绝而重复数组保留顺序。
func TestBindRejectsAmbiguousParameterValues(t *testing.T) {
	var target struct {
		IDs  []int `query:"id"`
		Page int   `query:"page"`
	}
	req := bindingRequest(t, `{}`)
	req.Raw().URL.RawQuery = "id=1&id=2&page=1&page=2"
	if err := Bind(req, &target); err == nil {
		t.Fatal("重复标量未拒绝")
	}
	req.Raw().URL.RawQuery = "id=1&id=2&page=3"
	if err := Bind(req, &target); err != nil || !reflect.DeepEqual(target.IDs, []int{1, 2}) || target.Page != 3 {
		t.Fatalf("多值绑定失败: %#v %v", target, err)
	}
}
