package exception

import (
	"fmt"
	"net/http"
	"net/http/httptest"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

// ReportAndRender 统一异常上报和渲染顺序，并将处理器自身的 panic 交回宿主处理。
func ReportAndRender(handler Handler, writer http.ResponseWriter, request *http.Request, recovered any) (err error) {
	defer func() {
		if nested := recover(); nested != nil {
			if cause, ok := nested.(error); ok {
				err = fmt.Errorf("异常渲染 panic: %w", cause)
			} else {
				err = fmt.Errorf("异常渲染 panic: %v", nested)
			}
		}
	}()
	if handler == nil {
		handler = &Handle{}
	}
	if request == nil {
		request = httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
	}
	handler.Report(recovered)
	return handler.Render(writer, request, recovered)
}

// RenderResponse 先在内存中完成渲染；任何失败都丢弃部分实体和响应头，返回安全的 500。
func RenderResponse(handler Handler, request *http.Request, recovered any) (*fwcontext.Response, error) {
	recorder := httptest.NewRecorder()
	if err := ReportAndRender(handler, recorder, request, recovered); err != nil {
		return InternalServerErrorResponse(), err
	}
	return ResponseFromRecorder(recorder), nil
}

// ResponseFromRecorder 将标准 HTTP 渲染结果适配为框架响应，协议级头由 HTTP 宿主管理。
func ResponseFromRecorder(recorder *httptest.ResponseRecorder) *fwcontext.Response {
	if recorder == nil {
		return InternalServerErrorResponse()
	}
	status := recorder.Code
	if status == 0 {
		status = http.StatusOK
	}
	response := fwcontext.NewResponse().Code(status)
	for key, values := range recorder.Header() {
		if IsHostManagedHeader(key) {
			continue
		}
		for index, value := range values {
			if index == 0 {
				response.Header(key, value)
			} else {
				response.AddHeader(key, value)
			}
		}
	}
	return response.Content(recorder.Body.String())
}

// IsHostManagedHeader 标记不能从内存渲染器复制到最终连接的协议级响应头。
func IsHostManagedHeader(name string) bool {
	switch http.CanonicalHeaderKey(name) {
	case "Connection", "Content-Length", "Keep-Alive", "Proxy-Connection", "Te", "Trailer", "Transfer-Encoding", "Upgrade":
		return true
	default:
		return false
	}
}

// InternalServerErrorResponse 提供不含异常细节的统一兜底响应。
func InternalServerErrorResponse() *fwcontext.Response {
	return fwcontext.NewResponse().
		Header("Content-Type", "text/plain; charset=utf-8").
		Header("X-Content-Type-Options", "nosniff").
		Code(http.StatusInternalServerError).
		Content(http.StatusText(http.StatusInternalServerError))
}
