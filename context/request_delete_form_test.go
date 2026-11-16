package context

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestDeleteURLEncodedBodyFeedsEveryParameterAccessor(t *testing.T) {
	const form = "id=5&items=one&items=two&name=a%26b"
	raw := httptest.NewRequest(http.MethodDelete, "/items?id=query", strings.NewReader(form))
	raw.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	request := newRequestForTest(t, raw)
	if request.Delete("id") != "5" || request.Post("id") != "5" || request.Param("id") != "5" || request.Get("id") != "query" {
		t.Fatalf("表单正文或查询参数优先级错误: %#v", request.All())
	}
	values := request.All()
	items, _ := values["items"].([]string)
	if values["name"] != "a&b" || !slices.Equal(items, []string{"one", "two"}) {
		t.Fatalf("表单解码或重复参数丢失: %#v", values)
	}
	if body, err := request.Body(); err != nil || string(body) != form || request.ParseError() != nil {
		t.Fatalf("解析表单后应保留原始正文: %q, %v", body, err)
	}
}

func TestDeleteURLEncodedBodyRejectsMalformedEscape(t *testing.T) {
	raw := httptest.NewRequest(http.MethodDelete, "/items", strings.NewReader("id=5&bad=%xx"))
	raw.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request := newRequestForTest(t, raw)
	if value := request.Delete("id", "missing"); value != "missing" || !errors.Is(request.ParseError(), ErrInvalidFormBody) {
		t.Fatalf("错误正文不能返回部分参数: value=%q err=%v", value, request.ParseError())
	}
}
