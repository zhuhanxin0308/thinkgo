package middleware

import (
	"net/http"
	"net/http/httptest"

	"thinkgo/framework/context"
	"thinkgo/framework/exception"
	frameworkLog "thinkgo/framework/log"
)

// Recovery panic 恢复中间件。
// 捕获请求处理过程中的 panic，并统一转换成框架 Response。
type Recovery struct {
	App    exception.AppContract
	Log    exception.Logger
	TplDir string
}

// Handle 处理请求并把 panic 转成统一异常响应，避免重复抛出导致重复日志。
func (m *Recovery) Handle(req *context.Request, next func(*context.Request) *context.Response) (resp *context.Response) {
	defer func() {
		if recovered := recover(); recovered != nil {
			handler := &exception.Handle{
				App:    m.App,
				Log:    m.Log,
				TplDir: m.TplDir,
			}

			rawReq := req.Raw()
			if rawReq == nil {
				rawReq = httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
			}

			recorder := httptest.NewRecorder()
			if err := handler.Render(recorder, rawReq, recovered); err != nil && m.Log != nil {
				// ResponseRecorder 正常情况下不会写失败，此处仍保留诊断信息以防自定义实现异常。
				m.Log.ErrorCtx("渲染恢复异常失败", map[string]interface{}{"error": frameworkLog.SanitizeErrorText(err.Error())})
			}
			resp = responseFromRecorder(recorder)
		}
	}()

	resp = next(req)
	if resp == nil {
		return context.NewResponse()
	}
	return resp
}

// responseFromRecorder 把标准库 ResponseRecorder 转回框架 Response，便于继续走统一发送链路。
func responseFromRecorder(recorder *httptest.ResponseRecorder) *context.Response {
	resp := context.NewResponse()
	status := recorder.Code
	if status == 0 {
		status = http.StatusOK
	}
	resp.Code(status).Content(recorder.Body.String())

	for key, values := range recorder.Header() {
		for _, value := range values {
			resp.AddHeader(key, value)
		}
	}

	return resp
}
