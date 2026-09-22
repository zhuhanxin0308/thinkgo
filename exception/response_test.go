package exception

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type failingResponseHandler struct {
	reports int
	failure error
	panicOn string
}

func (handler *failingResponseHandler) Report(any) {
	handler.reports++
	if handler.panicOn == "report" {
		panic(handler.failure)
	}
}

func (handler *failingResponseHandler) Render(writer http.ResponseWriter, _ *http.Request, _ any) error {
	writer.Header().Set("X-Partial", "private")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte("private details"))
	if handler.panicOn == "render" {
		panic("render failed")
	}
	return handler.failure
}

// TestRenderResponseDiscardsFailedOutput 验证渲染返回错误与上报、渲染 panic 均不能发布部分成功响应。
func TestRenderResponseDiscardsFailedOutput(t *testing.T) {
	for _, phase := range []string{"", "report", "render"} {
		t.Run("failure_"+phase, func(t *testing.T) {
			failure := errors.New("private renderer failure")
			handler := &failingResponseHandler{failure: failure, panicOn: phase}
			response, err := RenderResponse(handler, nil, errors.New("business failure"))
			if err == nil || phase != "render" && !errors.Is(err, failure) {
				t.Fatalf("渲染失败原因没有保留: %v", err)
			}
			if response.GetStatus() != http.StatusInternalServerError || string(response.GetBody()) != http.StatusText(http.StatusInternalServerError) {
				t.Fatalf("渲染失败泄露了部分输出: %d %s", response.GetStatus(), response.GetBody())
			}
			if response.GetHeader("X-Partial") != "" || handler.reports != 1 {
				t.Fatal("失败响应保留了部分头或重复上报")
			}
		})
	}
}

// TestResponseAdapterPreservesApplicationHeaders 验证业务响应头保留多值，协议级响应头由宿主重建。
func TestResponseAdapterPreservesApplicationHeaders(t *testing.T) {
	recorder := httptest.NewRecorder()
	recorder.Header().Add("Set-Cookie", "a=1")
	recorder.Header().Add("Set-Cookie", "b=2")
	recorder.Header().Set("Content-Length", "999")
	recorder.Header().Set("Transfer-Encoding", "chunked")
	recorder.WriteHeader(http.StatusForbidden)
	_, _ = recorder.WriteString("denied")
	response := ResponseFromRecorder(recorder)
	if response.GetStatus() != http.StatusForbidden || string(response.GetBody()) != "denied" {
		t.Fatal("适配器改变了异常状态或正文")
	}
	if response.GetHeader("Content-Length") != "" || response.GetHeader("Transfer-Encoding") != "" {
		t.Fatal("适配器复制了由连接宿主管理的响应头")
	}
	if values := response.Headers().Values("Set-Cookie"); len(values) != 2 {
		t.Fatalf("适配器丢失多值 Cookie: %v", values)
	}
	if ResponseFromRecorder(nil).GetStatus() != http.StatusInternalServerError {
		t.Fatal("缺少渲染结果没有返回 500")
	}
}
