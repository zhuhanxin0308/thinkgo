package http

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/route"
)

const issue151Payload = "effective-head-response"

type issue151Controller struct{}

func (*issue151Controller) Show() *fwcontext.Response {
	return fwcontext.NewResponse().Content(issue151Payload)
}

func TestEffectiveHEADIsSharedByEveryHandlerType(t *testing.T) {
	for _, compression := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "compressed"}[compression], func(t *testing.T) {
			app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": compression, "min_size": 1})
			app.BindFactory("Issue151", func() interface{} { return &issue151Controller{} })
			mustHTTPMiddleware(t, app).Pipe(issue151MethodMiddleware)
			jsonHandler, err := route.NewJSONHandler(func() map[string]string {
				return map[string]string{"message": issue151Payload}
			}, http.StatusOK)
			if err != nil {
				t.Fatal(err)
			}
			handlers := []struct {
				name    string
				handler route.HandlerFunc
			}{
				{name: "callback", handler: func(*fwcontext.Request) *fwcontext.Response { return fwcontext.NewResponse().Content(issue151Payload) }},
				{name: "typed callback", handler: func() string { return issue151Payload }},
				{name: "json", handler: jsonHandler},
				{name: "controller", handler: "Issue151@Show"},
				{name: "standard", handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					w.Header().Set("X-Handler-Method", req.Method)
					_, _ = io.WriteString(w, issue151Payload)
				})},
			}
			router := mustHTTPRoute(t, app)
			for index, handler := range handlers {
				if _, err := router.Rule("/api/head/"+strconv.Itoa(index), handler.handler, "GET|HEAD"); err != nil {
					t.Fatal(err)
				}
			}
			host := newTestHTTPHandler(t, app)
			for index, handler := range handlers {
				t.Run(handler.name, func(t *testing.T) {
					path := "http://127.0.0.1/api/head/" + strconv.Itoa(index)
					for _, test := range []struct {
						name, raw, override, set, effective string
					}{
						{name: "raw HEAD", raw: http.MethodHead, effective: http.MethodHead},
						{name: "GET override", raw: http.MethodGet, override: http.MethodHead, effective: http.MethodHead},
						{name: "POST override", raw: http.MethodPost, override: http.MethodHead, effective: http.MethodHead},
						{name: "middleware HEAD", raw: http.MethodGet, set: http.MethodHead, effective: http.MethodHead},
						{name: "raw HEAD overridden GET", raw: http.MethodHead, override: http.MethodGet, effective: http.MethodGet},
						{name: "raw HEAD middleware GET", raw: http.MethodHead, set: http.MethodGet, effective: http.MethodGet},
					} {
						t.Run(test.name, func(t *testing.T) {
							req := httptest.NewRequest(test.raw, path, nil)
							req.Header.Set("Accept-Encoding", "gzip")
							req.Header.Set("X-HTTP-Method-Override", test.override)
							req.Header.Set("X-Test-Set-Method", test.set)
							recorder := httptest.NewRecorder()
							host.ServeHTTP(recorder, req)
							assertIssue151HEAD(t, recorder, http.StatusOK)
							if req.Method != test.raw || recorder.Header().Get("X-Origin-Method") != test.raw || recorder.Header().Get("X-Effective-Method") != test.effective {
								t.Fatalf("方法审计或有效路由方法变化: raw=%q headers=%v", req.Method, recorder.Header())
							}
							if handler.name == "standard" && recorder.Header().Get("X-Handler-Method") != test.effective {
								t.Fatalf("标准处理器仍须看到有效方法: %v", recorder.Header())
							}
						})
					}
					control := httptest.NewRequest(http.MethodGet, path, nil)
					control.Header.Set("Accept-Encoding", "gzip")
					recorder := httptest.NewRecorder()
					host.ServeHTTP(recorder, control)
					body := recorder.Body.Bytes()
					if encoding := recorder.Header().Get("Content-Encoding"); encoding != "" {
						body, err = decodeCompressedTestBody(encoding, body)
						if err != nil {
							t.Fatal(err)
						}
					}
					if recorder.Code != http.StatusOK || !strings.Contains(string(body), issue151Payload) {
						t.Fatalf("普通 GET 正文被改变: status=%d body=%q", recorder.Code, body)
					}
				})
			}
		})
	}
}

func TestEffectiveHEADSuppressesEntityVariantsAndErrors(t *testing.T) {
	for _, compression := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "compressed"}[compression], func(t *testing.T) {
			basePath := t.TempDir()
			filePath := filepath.Join(basePath, "download.txt")
			if err := os.WriteFile(filePath, []byte(issue151Payload), 0o600); err != nil {
				t.Fatal(err)
			}
			app := newTestHTTPApp(t, basePath, map[string]interface{}{"enable": compression, "min_size": 1})
			tests := []struct {
				name   string
				status int
				create func() *fwcontext.Response
			}{
				{name: "stream", status: http.StatusOK, create: func() *fwcontext.Response {
					return fwcontext.NewResponse().Header("Content-Type", "text/plain").Stream(func(w io.Writer) error {
						if _, err := io.WriteString(w, issue151Payload); err != nil {
							return err
						}
						if flusher, ok := w.(interface{ FlushError() error }); ok {
							if err := flusher.FlushError(); err != nil {
								return err
							}
						}
						_, err := io.WriteString(w, "tail")
						return err
					})
				}},
				{name: "chunks", status: http.StatusOK, create: func() *fwcontext.Response {
					return fwcontext.NewResponse().Header("Content-Type", "text/plain").Chunk([][]byte{[]byte(issue151Payload), []byte("tail")})
				}},
				{name: "file", status: http.StatusOK, create: func() *fwcontext.Response {
					return fwcontext.NewResponse().Download(filePath, "download.txt")
				}},
				{name: "missing file", status: http.StatusNotFound, create: func() *fwcontext.Response {
					return fwcontext.NewResponse().Download(filePath+".missing", "missing.txt")
				}},
				{name: "error response", status: http.StatusBadGateway, create: func() *fwcontext.Response {
					return fwcontext.NewResponse().Code(http.StatusBadGateway).Content("error body")
				}},
				{name: "stream failure", status: http.StatusInternalServerError, create: func() *fwcontext.Response {
					return fwcontext.NewResponse().Stream(func(io.Writer) error { return errors.New("stream unavailable") })
				}},
				{name: "panic", status: http.StatusInternalServerError, create: func() *fwcontext.Response { panic("handler failure") }},
			}
			router := mustHTTPRoute(t, app)
			for index, test := range tests {
				if _, err := router.Head("/api/entity/"+strconv.Itoa(index), func(*fwcontext.Request) *fwcontext.Response { return test.create() }); err != nil {
					t.Fatal(err)
				}
			}
			host := newTestHTTPHandler(t, app)
			for index, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					for _, rawMethod := range []string{http.MethodHead, http.MethodGet} {
						req := httptest.NewRequest(rawMethod, "http://127.0.0.1/api/entity/"+strconv.Itoa(index), nil)
						req.Header.Set("X-HTTP-Method-Override", http.MethodHead)
						req.Header.Set("Accept-Encoding", "gzip")
						recorder := httptest.NewRecorder()
						host.ServeHTTP(recorder, req)
						assertIssue151HEAD(t, recorder, test.status)
						if test.name == "file" && recorder.Header().Get("Content-Length") != strconv.Itoa(len(issue151Payload)) {
							t.Fatalf("HEAD 文件响应须保留表示长度: %v", recorder.Header())
						}
					}
				})
			}
		})
	}
}

func issue151MethodMiddleware(req *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
	if method := req.Raw().Header.Get("X-Test-Set-Method"); method != "" {
		req.SetMethod(method)
	}
	if writer, ok := req.ResponseWriter(); ok {
		writer.Header().Set("X-Origin-Method", req.Method(true))
		writer.Header().Set("X-Effective-Method", req.Method())
	}
	return next(req)
}

func assertIssue151HEAD(t *testing.T, recorder *httptest.ResponseRecorder, status int) {
	t.Helper()
	if recorder.Code != status || recorder.Body.Len() != 0 || recorder.Header().Get("Content-Encoding") != "" {
		t.Fatalf("HEAD 响应状态/实体策略错误: status=%d want=%d encoding=%q body=%q", recorder.Code, status, recorder.Header().Get("Content-Encoding"), recorder.Body.String())
	}
}
