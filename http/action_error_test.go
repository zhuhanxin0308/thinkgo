package http

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
	"github.com/zhuhanxin0308/thinkgo/framework/exception"
	"github.com/zhuhanxin0308/thinkgo/framework/route"
)

// TestActionReturnedExceptionsKeepHTTPMeaning 验证返回的异常与抛出的异常使用同一响应边界。
func TestActionReturnedExceptionsKeepHTTPMeaning(t *testing.T) {
	application := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	handler := newTestHTTPHandler(t, application)
	for _, test := range []struct {
		name    string
		err     error
		status  int
		message string
	}{
		{"http", fmt.Errorf("wrapped: %w", exception.NewHttpException(http.StatusNotFound, "资源不存在")), http.StatusNotFound, "资源不存在"},
		{"validation", exception.NewValidateException("邮箱格式错误", "email"), http.StatusUnprocessableEntity, "邮箱格式错误"},
		{"internal", fmt.Errorf("private database credentials"), http.StatusInternalServerError, "Internal Server Error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			router := route.NewRouter()
			registered, err := router.Get("/errors", func() error { return test.err })
			if err != nil {
				t.Fatal(err)
			}
			raw := httptest.NewRequest(http.MethodGet, "/errors", nil)
			raw.Header.Set("Accept", "application/json")
			response := handler.dispatch(registered, fwcontext.MustNewRequest(raw))
			if response.GetStatus() != test.status || !strings.Contains(string(response.GetBody()), test.message) {
				t.Fatalf("错误响应不一致: status=%d body=%s", response.GetStatus(), response.GetBody())
			}
			if strings.Contains(string(response.GetBody()), "private database") {
				t.Fatal("内部错误泄露实现细节")
			}
		})
	}
}
