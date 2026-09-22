package route

import (
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework/binding"
	"github.com/zhuhanxin0308/thinkgo/framework/context"
)

// TestJSONHandlerValidatesContractBeforeRegistration 验证错误状态与返回签名不会进入运行期。
func TestJSONHandlerValidatesContractBeforeRegistration(t *testing.T) {
	type input struct{ binding.Input }
	for _, callback := range []any{
		nil, "Index@Read", (func() string)(nil), func(...string) {},
		func() (string, string) { return "", "" },
		func() (error, error) { return nil, nil },
		func(input, input) {}, func() any { return nil },
		func() *context.Response { return nil },
	} {
		if _, err := NewJSONHandler(callback, http.StatusOK); !errors.Is(err, ErrInvalidRouteHandler) {
			t.Fatalf("非法签名 %T 未拒绝: %v", callback, err)
		}
	}
	for _, status := range []int{http.StatusContinue, http.StatusBadRequest, http.StatusNoContent, http.StatusResetContent} {
		if _, err := NewJSONHandler(func() string { return "" }, status); !errors.Is(err, ErrInvalidRouteHandler) {
			t.Fatalf("非法状态 %d 未拒绝: %v", status, err)
		}
	}
	callback := func() string { return "ok" }
	handler, err := NewJSONHandler(callback, 0)
	if err != nil || handler.SuccessStatus() != http.StatusOK || handler.OutputType() != reflect.TypeFor[string]() || handler.Callback().(func() string)() != "ok" {
		t.Fatalf("默认数据契约错误: %#v %v", handler, err)
	}
	for _, value := range []*JSONHandler{nil, {}} {
		if _, err := NewRouter().Get("/invalid", value); !errors.Is(err, ErrInvalidRouteHandler) {
			t.Fatalf("空 JSON 处理器未拒绝: %v", err)
		}
	}
}
