package middleware

import (
	"net/http"
	"net/http/httptest"

	"github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/exception"
	"github.com/zhuhanxin0308/thinkgo/v3/internal/cleanup"
	frameworkLog "github.com/zhuhanxin0308/thinkgo/v3/log"
)

// Recovery panic 恢复中间件。
// 捕获请求处理过程中的 panic，并统一转换成框架 Response。
type Recovery struct {
	App     exception.AppContract
	Log     exception.Logger
	TplDir  string
	Handler exception.Handler
}

// Handle 处理请求并把 panic 转成统一异常响应，避免重复抛出导致重复日志。
func (m *Recovery) Handle(req *context.Request, next func(*context.Request) *context.Response) *context.Response {
	return cleanup.Catch(func() *context.Response {
		if resp := next(req); resp != nil {
			return resp
		}
		return context.NewResponse()
	}, func(recovered any) *context.Response {
		if recovered == http.ErrAbortHandler {
			panic(http.ErrAbortHandler)
		}
		if writer, exists := req.ResponseWriter(); exists {
			if resetter, ok := writer.(interface{ ResetUncommitted() bool }); ok && !resetter.ResetUncommitted() {
				// 已提交响应无法改写为错误页，必须保留客户端可见的传输失败。
				panic(http.ErrAbortHandler)
			}
		}
		handler := m.Handler
		if handler == nil {
			handler = &exception.Handle{App: m.App, Log: m.Log, TplDir: m.TplDir}
		}
		resp, err := exception.RenderResponse(handler, req.Raw(), recovered)
		if err != nil && m.Log != nil {
			// 自定义处理器已完成上报和渲染，这里仅记录渲染失败。
			if filter, ok := m.Log.(interface{ IsLevelEnabled(string) bool }); !ok || filter.IsLevelEnabled(frameworkLog.LevelError) {
				m.Log.ErrorCtx("渲染恢复异常失败", map[string]interface{}{"error": frameworkLog.SanitizeErrorText(err.Error())})
			}
		}
		return resp
	})
}

// responseFromRecorder 把标准库 ResponseRecorder 转回框架 Response，便于继续走统一发送链路。
func responseFromRecorder(recorder *httptest.ResponseRecorder) *context.Response {
	return exception.ResponseFromRecorder(recorder)
}
