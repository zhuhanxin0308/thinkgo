package http

import (
	"bufio"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

// TestStreamFailureAbortsRealTCPResponse 验证已提交的失败响应不会以正常 EOF 冒充完整成功。
func TestStreamFailureAbortsRealTCPResponse(t *testing.T) {
	for _, mode := range []string{"plain", "gzip", "abort-handler", "committed-panic"} {
		t.Run(mode, func(t *testing.T) {
			app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": mode == "gzip", "min_size": 1, "levels": map[string]interface{}{"gzip": 1}})
			if mode == "abort-handler" || mode == "committed-panic" {
				_, err := mustHTTPRoute(t, app).Get("/failure", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					_, _ = io.WriteString(w, "partial\n")
					_ = http.NewResponseController(w).Flush()
					if mode == "abort-handler" {
						panic(http.ErrAbortHandler)
					}
					panic("upstream failed")
				}))
				if err != nil {
					t.Fatal(err)
				}
			} else {
				_, err := mustHTTPRoute(t, app).Get("/failure", func(req *fwcontext.Request) *fwcontext.Response {
					return fwcontext.NewResponse().Stream(func(w io.Writer) error {
						_, _ = io.WriteString(w, strings.Repeat("partial", 1024))
						writer, _ := req.ResponseWriter()
						_ = http.NewResponseController(writer).Flush()
						return errors.New("upstream failed")
					})
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			server := httptest.NewServer(newTestHTTPHandler(t, app))
			defer server.Close()
			response, err := server.Client().Get(server.URL + "/failure")
			if err != nil {
				t.Fatalf("客户端应先收到已刷新的响应头: %v", err)
			}
			defer response.Body.Close()
			body, readErr := io.ReadAll(response.Body)
			if len(body) == 0 || readErr == nil {
				t.Fatalf("部分响应必须以传输错误终止: status=%d bytes=%d error=%v", response.StatusCode, len(body), readErr)
			}
		})
	}
}

// TestStreamFlushReachesTCPBeforeCallbackReturns 验证 SSE 小事件在回调结束前真实到达客户端。
func TestStreamFlushReachesTCPBeforeCallbackReturns(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": true, "min_size": 1024, "levels": map[string]interface{}{"gzip": 1}})
	release := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	defer finish()
	_, err := mustHTTPRoute(t, app).Get("/events", func(*fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().ContentType("text/event-stream").Stream(func(w io.Writer) error {
			_, writeErr := io.WriteString(w, "data: first\n\n")
			if writeErr != nil {
				return writeErr
			}
			flusher, ok := w.(interface{ FlushError() error })
			if !ok {
				return errors.New("流式回调缺少 FlushError")
			}
			if err := flusher.FlushError(); err != nil {
				return err
			}
			<-release
			_, writeErr = io.WriteString(w, "data: final\n\n")
			return writeErr
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newTestHTTPHandler(t, app))
	defer server.Close()
	first := make(chan string, 1)
	go func() {
		response, err := server.Client().Get(server.URL + "/events")
		if err != nil {
			first <- err.Error()
			return
		}
		defer response.Body.Close()
		line, err := bufio.NewReader(response.Body).ReadString('\n')
		if err != nil {
			first <- err.Error()
			return
		}
		first <- line
	}()
	select {
	case line := <-first:
		if line != "data: first\n" {
			t.Fatalf("首个事件内容错误: %q", line)
		}
	case <-time.After(2 * time.Second):
		finish()
		t.Fatal("回调尚未结束时客户端没有收到首个事件")
	}
	finish()
}
