package http

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	framework "github.com/zhuhanxin0308/thinkgo/v3"
	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/log"
)

func TestEffectiveHEADCoversMiddlewareWritesAndCommitFailure(t *testing.T) {
	for _, compression := range []bool{false, true} {
		for _, kind := range []string{"direct", "short response", "commit failure", "panic"} {
			t.Run(map[bool]string{false: "plain/", true: "compressed/"}[compression]+kind, func(t *testing.T) {
				app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": compression, "min_size": 1})
				failure := errors.New("commit unavailable")
				status := http.StatusAccepted
				if kind == "commit failure" || kind == "panic" {
					status = http.StatusInternalServerError
				}
				mustHTTPMiddleware(t, app).Pipe(func(req *fwcontext.Request, _ func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
					req.SetMethod(http.MethodHead)
					if req.Method(true) != http.MethodGet {
						t.Fatalf("SetMethod 改变了审计方法: %q", req.Method(true))
					}
					switch kind {
					case "short response":
						return fwcontext.NewResponse().Code(status).Content(issue151Payload)
					case "panic":
						panic("middleware failure")
					}
					writer, ok := req.ResponseWriter()
					if !ok {
						t.Fatal("中间件缺少共同 writer")
					}
					writer.Header().Set("Content-Type", "text/plain")
					if kind == "commit failure" {
						registrar, ok := writer.(interface {
							BeforeCommit(func(http.Header) error) error
						})
						if !ok {
							t.Fatal("可选接口门面丢失提交钩子")
						}
						if err := registrar.BeforeCommit(func(http.Header) error { return failure }); err != nil {
							t.Fatal(err)
						}
					}
					writer.WriteHeader(http.StatusAccepted)
					written, err := io.WriteString(writer, issue151Payload)
					if kind == "commit failure" {
						if written != 0 || !errors.Is(err, failure) {
							t.Fatalf("HEAD 抑制不得吞掉提交错误: written=%d err=%v", written, err)
						}
					} else if err != nil || written != len(issue151Payload) {
						t.Fatalf("HEAD Write 应确认消费全部字节: written=%d err=%v", written, err)
					}
					return fwcontext.NewCommittedResponse(status)
				})
				raw := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/short", nil)
				raw.Header.Set("Accept-Encoding", "gzip")
				recorder := httptest.NewRecorder()
				newTestHTTPHandler(t, app).ServeHTTP(recorder, raw)
				assertIssue151HEAD(t, recorder, status)
				if raw.Method != http.MethodGet {
					t.Fatalf("共同 writer 改写了原始请求: %q", raw.Method)
				}
			})
		}
	}
}

func TestHEADFastPathsKeepCommitFailureStatusAndLogging(t *testing.T) {
	for _, compression := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "compressed"}[compression], func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "report.txt")
			if err := os.WriteFile(path, []byte(issue151Payload), 0o600); err != nil {
				t.Fatal(err)
			}
			app := newTestHTTPApp(t, root, map[string]interface{}{"enable": compression, "min_size": 1})
			driver := &accessLogDriver{}
			logger := log.NewLog(driver)
			t.Cleanup(func() { _ = logger.Close() })
			if err := app.Instance(string(framework.ServiceLog), logger); err != nil {
				t.Fatal(err)
			}
			failure := &testCommitFailure{status: http.StatusConflict}
			mustHTTPMiddleware(t, app).Pipe(func(req *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
				writer, _ := req.ResponseWriter()
				hook := writer.(interface {
					BeforeCommit(func(http.Header) error) error
				})
				if err := hook.BeforeCommit(func(http.Header) error { return failure }); err != nil {
					t.Fatal(err)
				}
				return next(req)
			})
			router := mustHTTPRoute(t, app)
			for route, response := range map[string]func() *fwcontext.Response{
				"/buffer":   func() *fwcontext.Response { return fwcontext.NewResponse().Content(issue151Payload) },
				"/download": func() *fwcontext.Response { return fwcontext.NewResponse().Download(path, "report.txt") },
			} {
				if _, err := router.Head(route, func(*fwcontext.Request) *fwcontext.Response { return response() }); err != nil {
					t.Fatal(err)
				}
			}
			host := newTestHTTPHandler(t, app)
			for _, route := range []string{"/buffer", "/download"} {
				req := httptest.NewRequest(http.MethodHead, "http://127.0.0.1"+route, nil)
				req.Header.Set("Accept-Encoding", "gzip")
				recorder := httptest.NewRecorder()
				host.ServeHTTP(recorder, req)
				assertIssue151HEAD(t, recorder, http.StatusConflict)
				if recorder.Result().Header.Get("Content-Disposition") != "" || recorder.Result().Header.Get("Content-Length") != "" {
					t.Fatalf("失败响应不得继承业务响应元数据: %v", recorder.Result().Header)
				}
			}
			if err := logger.Flush(t.Context()); err != nil {
				t.Fatal(err)
			}
			var failures int
			for _, entry := range driver.allEntries() {
				if entry.Level == log.LevelError && strings.Contains(entry.FormatEntry(), failure.Error()) {
					failures++
				}
			}
			if failures != 2 {
				t.Fatalf("两个 HEAD 提交错误应各记录一次，实际为 %d", failures)
			}
		})
	}
}

func TestStatusWriterFreezesHEADAtFirstFinalCommit(t *testing.T) {
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/", nil))
	recorder := &responseProtocolRecorder{}
	writer := newStatusTrackingResponseWriter(recorder)
	writer.method = request
	hookCalls := 0
	if err := writer.BeforeCommit(func(http.Header) error {
		hookCalls++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	writer.WriteHeader(http.StatusEarlyHints)
	request.SetMethod(http.MethodHead)
	writer.WriteHeader(http.StatusAccepted)
	request.SetMethod(http.MethodGet)
	if written, err := writer.Write([]byte(issue151Payload)); err != nil || written != len(issue151Payload) {
		t.Fatalf("提交后的 HEAD Write 错误: written=%d err=%v", written, err)
	}
	if len(recorder.body) != 0 || hookCalls != 1 || !reflect.DeepEqual(recorder.statuses, []int{http.StatusEarlyHints, http.StatusAccepted}) {
		t.Fatalf("1xx 不得冻结方法，最终 HEAD 决定不得被后续变更撤回: statuses=%v body=%q hooks=%d", recorder.statuses, recorder.body, hookCalls)
	}
}

func TestStatusWriterDoesNotChangeEntityPolicyAfterGETCommit(t *testing.T) {
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/", nil))
	recorder := httptest.NewRecorder()
	writer := newStatusTrackingResponseWriter(recorder)
	writer.method = request
	if _, err := writer.Write([]byte("first")); err != nil {
		t.Fatal(err)
	}
	request.SetMethod(http.MethodHead)
	if _, err := writer.Write([]byte("second")); err != nil {
		t.Fatal(err)
	}
	if recorder.Body.String() != "firstsecond" {
		t.Fatalf("提交后方法变更不得截断已开始的 GET 实体: %q", recorder.Body.String())
	}
}

func TestOriginalHEADEarlyKernelErrorHasNoEntity(t *testing.T) {
	var host *Http
	recorder := httptest.NewRecorder()
	host.ServeHTTP(recorder, httptest.NewRequest(http.MethodHead, "http://example.com/", nil))
	assertIssue151HEAD(t, recorder, http.StatusInternalServerError)
}
