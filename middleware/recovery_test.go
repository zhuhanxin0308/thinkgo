package middleware

import (
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/exception"
)

type recoveryTestApp struct {
	debug bool
}

func TestRecoveryReportsNilPanicOnce(t *testing.T) {
	for _, mode := range []string{"0", "1"} {
		t.Run("panicnil="+mode, func(t *testing.T) {
			t.Setenv("GODEBUG", os.Getenv("GODEBUG")+",panicnil="+mode)
			logger := &recoveryTestLogger{}
			req := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "/", nil))
			resp := (&Recovery{Log: logger}).Handle(req, func(*fwcontext.Request) *fwcontext.Response { panic(nil) })
			if resp == nil || resp.GetStatus() != http.StatusInternalServerError || len(logger.errorCalls) != 1 {
				t.Fatalf("nil panic 必须产生一次报告与 500 响应：响应=%v，报告=%d", resp, len(logger.errorCalls))
			}
		})
	}
}

type committedRecoveryWriter struct{ *httptest.ResponseRecorder }

func (committedRecoveryWriter) ResetUncommitted() bool { return false }

// 业务中止、已提交响应和协程退出都不能在恢复路径上产生第二份响应或错误报告。
func TestRecoveryPreservesAbortsAndGoexit(t *testing.T) {
	t.Setenv("GODEBUG", os.Getenv("GODEBUG")+",panicnil=1")
	for _, mode := range []string{"abort", "committed", "goexit"} {
		t.Run(mode, func(t *testing.T) {
			logger := &recoveryTestLogger{}
			writer := httptest.NewRecorder()
			var target http.ResponseWriter = writer
			if mode == "committed" {
				writer.WriteHeader(http.StatusAccepted)
				target = committedRecoveryWriter{writer}
			}
			req := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "/", nil), fwcontext.WithResponseWriter(target))
			done := make(chan struct{})
			var returned bool
			var recovered any
			go func() {
				defer close(done)
				defer func() { recovered = recover() }()
				(&Recovery{Log: logger}).Handle(req, func(*fwcontext.Request) *fwcontext.Response {
					switch mode {
					case "abort":
						panic(http.ErrAbortHandler)
					case "committed":
						panic(nil)
					}
					runtime.Goexit()
					return nil
				})
				returned = true
			}()
			<-done
			if returned || (mode != "goexit" && recovered != http.ErrAbortHandler) ||
				(mode == "goexit" && recovered != nil) || len(logger.errorCalls) != 0 {
				t.Fatalf("退出方式或报告错误：返回=%t，异常=%v，报告=%d", returned, recovered, len(logger.errorCalls))
			}
			if mode == "committed" && writer.Code != http.StatusAccepted {
				t.Fatalf("已提交状态被覆盖：%d", writer.Code)
			}
		})
	}
}

func (a *recoveryTestApp) IsDebug() bool {
	return a.debug
}

type recoveryLogCall struct {
	msg string
	ctx map[string]interface{}
}

type recoveryTestLogger struct {
	errorCalls   []recoveryLogCall
	warningCalls []recoveryLogCall
}

func (l *recoveryTestLogger) Error(msg string) {
	l.errorCalls = append(l.errorCalls, recoveryLogCall{msg: msg})
}

func (l *recoveryTestLogger) ErrorCtx(msg string, ctx map[string]interface{}) {
	l.errorCalls = append(l.errorCalls, recoveryLogCall{msg: msg, ctx: ctx})
}

func (l *recoveryTestLogger) Warning(msg string) {
	l.warningCalls = append(l.warningCalls, recoveryLogCall{msg: msg})
}

func (l *recoveryTestLogger) WarningCtx(msg string, ctx map[string]interface{}) {
	l.warningCalls = append(l.warningCalls, recoveryLogCall{msg: msg, ctx: ctx})
}

// TestRecoveryConvertsHttpExceptionToResponse 验证 Recovery 会把 panic 的 HTTP 异常转换成响应，
// 而不是再次 panic 给上层，避免重复日志与堆栈丢失。
func TestRecoveryConvertsHttpExceptionToResponse(t *testing.T) {
	logger := &recoveryTestLogger{}
	recovery := &Recovery{
		App: &recoveryTestApp{debug: false},
		Log: logger,
	}
	req := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/api/recovery", nil))
	req.Raw().Header.Set("Accept", "application/json")

	var (
		resp      *fwcontext.Response
		recovered interface{}
	)
	func() {
		defer func() {
			recovered = recover()
		}()
		resp = recovery.Handle(req, func(req *fwcontext.Request) *fwcontext.Response {
			panic(exception.NewHttpException(http.StatusForbidden, "forbidden"))
		})
	}()

	if recovered != nil {
		t.Fatalf("Recovery 不应再次 panic，实际为 %v", recovered)
	}
	if resp == nil {
		t.Fatal("Recovery 应返回异常响应，而不是 nil")
	}
	if resp.GetStatus() != http.StatusForbidden {
		t.Fatalf("HTTP 异常应保留原始状态码 403，实际为 %d", resp.GetStatus())
	}
	body := string(resp.GetBody())
	if !strings.Contains(body, `"code":403`) || !strings.Contains(body, `"msg":"forbidden"`) {
		t.Fatalf("Recovery 返回的 JSON 异常响应不正确，实际为 %s", body)
	}
	if len(logger.warningCalls) != 1 || len(logger.errorCalls) != 0 {
		t.Fatalf("HTTP 4xx 异常应只记录 1 次 warning，实际 warning=%d error=%d", len(logger.warningCalls), len(logger.errorCalls))
	}
}

// TestRecoveryReportsUnexpectedPanicOnce 验证 Recovery 对系统 panic 只记录一次异常，
// 并返回统一 500 响应。
func TestRecoveryReportsUnexpectedPanicOnce(t *testing.T) {
	logger := &recoveryTestLogger{}
	recovery := &Recovery{
		App: &recoveryTestApp{debug: false},
		Log: logger,
	}
	req := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/api/recovery", nil))
	req.Raw().Header.Set("Accept", "application/json")

	var (
		resp      *fwcontext.Response
		recovered interface{}
	)
	func() {
		defer func() {
			recovered = recover()
		}()
		resp = recovery.Handle(req, func(req *fwcontext.Request) *fwcontext.Response {
			panic("boom")
		})
	}()

	if recovered != nil {
		t.Fatalf("系统 panic 也不应被 Recovery 二次抛出，实际为 %v", recovered)
	}
	if resp == nil || resp.GetStatus() != http.StatusInternalServerError {
		t.Fatalf("系统 panic 应转换成 500 响应，实际响应为 %#v", resp)
	}
	if len(logger.errorCalls) != 1 {
		t.Fatalf("系统 panic 应只记录 1 次 error，实际为 %d", len(logger.errorCalls))
	}
	if len(logger.warningCalls) != 0 {
		t.Fatalf("系统 panic 不应记录 warning，实际为 %d", len(logger.warningCalls))
	}
}
