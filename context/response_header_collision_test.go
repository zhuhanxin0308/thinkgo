package context

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestResponseHeaderRejectsNormalizedCollisionsBeforeApplyingBatch(t *testing.T) {
	for _, headers := range []interface{}{
		map[string]string{"X-Frame-Options": "DENY", "x-frame-options": "SAMEORIGIN", "X-Batch": "partial"},
		map[string]interface{}{"Content-Type": "text/plain", " content-type ": "application/json", "X-Batch": "partial"},
		http.Header{"X-Frame-Options": {"DENY"}, " x-frame-options ": {"DENY"}, "X-Batch": {"partial"}},
	} {
		response := NewResponse().Header("X-Frame-Options", "DENY")
		before := response.Headers()
		if got := response.Header(headers); got != response {
			t.Fatal("批量 Header 应维持链式调用")
		}
		if !errors.Is(response.Error(), ErrInvalidResponseHeader) || !strings.Contains(response.Error().Error(), "重复") {
			t.Fatalf("重复规范头应明确报错: %v", response.Error())
		}
		if !reflect.DeepEqual(response.Headers(), before) {
			t.Fatalf("拒绝的批量头改变了响应: %#v", response.Headers())
		}
		recorder := httptest.NewRecorder()
		if err := response.Send(recorder); !errors.Is(err, ErrInvalidResponseHeader) {
			t.Fatalf("实际发送未报告头部冲突: %v", err)
		}
		if got := recorder.Result().Header; !reflect.DeepEqual(got, before) {
			t.Fatalf("拒绝的批量头进入了实际响应: %#v", got)
		}
	}
}

func TestResponseHeaderPreservesValuesUnderSingleCanonicalKey(t *testing.T) {
	response := NewResponse().Header(http.Header{" set-cookie ": {"one=1", "two=2"}}).AddHeader("Set-Cookie", "three=3")
	recorder := httptest.NewRecorder()
	if err := response.Send(recorder); err != nil {
		t.Fatal(err)
	}
	if got := recorder.Result().Header.Values("Set-Cookie"); !reflect.DeepEqual(got, []string{"one=1", "two=2", "three=3"}) {
		t.Fatalf("多值响应头顺序变化: %#v", got)
	}
}
