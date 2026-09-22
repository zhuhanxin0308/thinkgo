package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
)

type failingRecoveryRenderer struct {
	panicRender bool
	reports     int
}

func (handler *failingRecoveryRenderer) Report(any) { handler.reports++ }
func (handler *failingRecoveryRenderer) Render(writer http.ResponseWriter, _ *http.Request, _ any) error {
	writer.Header().Set("X-Partial", "incomplete")
	_, _ = writer.Write([]byte("sensitive partial response"))
	if handler.panicRender {
		panic("render failed")
	}
	return errors.New("render failed")
}

// TestRecoveryDiscardsFailedRendering 验证异常处理器失败时不会发送部分成功响应或重复上报原始异常。
func TestRecoveryDiscardsFailedRendering(t *testing.T) {
	for _, panicRender := range []bool{false, true} {
		handler := &failingRecoveryRenderer{panicRender: panicRender}
		request := fwcontext.MustNewRequest(httptest.NewRequest("GET", "/", nil))
		response := (&Recovery{Handler: handler}).Handle(request, func(*fwcontext.Request) *fwcontext.Response { panic("business failed") })
		if response.GetStatus() != http.StatusInternalServerError || response.GetHeader("X-Partial") != "" || handler.reports != 1 {
			t.Fatalf("失败渲染必须被丢弃: status=%d headers=%v reports=%d", response.GetStatus(), response.Headers(), handler.reports)
		}
	}
}
