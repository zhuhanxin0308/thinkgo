package http

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/event"
)

// TestRunReportsStaticConditionalStatus 验证直调 Run 时静态响应与 HttpEnd 使用已发送状态。
func TestRunReportsStaticConditionalStatus(t *testing.T) {
	base := t.TempDir()
	app := newTestHTTPApp(t, base, map[string]interface{}{"enable": false})
	public := filepath.Join(base, "public")
	if err := os.MkdirAll(public, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(public, "static.txt"), []byte("abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	var endStatus int
	if err := app.Event().Listen(event.EventHttpEnd, &event.SimpleListener{Handler: func(current event.Event) error {
		endStatus = current.(*event.HttpEndEvent).StatusCode
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	host := newTestHTTPHandler(t, app)
	for _, test := range []struct {
		header, value, body string
		status              int
	}{
		{"Range", "bytes=0-1", "ab", http.StatusPartialContent},
		{"If-Modified-Since", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat), "", http.StatusNotModified},
	} {
		raw := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/static.txt", nil)
		raw.Header.Set(test.header, test.value)
		writer := httptest.NewRecorder()
		request := fwcontext.MustNewRequest(raw, fwcontext.WithResponseWriter(writer))
		response := host.Run(request)
		host.End(response)
		if writer.Code != test.status || writer.Body.String() != test.body || !response.Committed() ||
			response.GetStatus() != test.status || endStatus != test.status {
			t.Errorf("%s 状态不一致: 线路=%d 响应=%d 事件=%d 期望=%d", test.header, writer.Code, response.GetStatus(), endStatus, test.status)
		}
	}
}

// TestEndReportsFinalTransmissionStatus 验证文件和流式发送形成的最终状态同步到结束事件与终结回调。
func TestEndReportsFinalTransmissionStatus(t *testing.T) {
	for _, testCase := range []struct {
		name string
		want int
	}{
		{name: "文件不存在", want: http.StatusNotFound},
		{name: "下载成功", want: http.StatusOK},
		{name: "安全路径拒绝", want: http.StatusForbidden},
		{name: "流式自定义状态", want: http.StatusAccepted},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			app := newTestHTTPApp(t, root, map[string]interface{}{"enable": false})
			target := filepath.Join(root, "download.txt")
			if testCase.name == "下载成功" {
				if err := os.WriteFile(target, []byte("内容"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			_, err := mustHTTPRoute(t, app).Get("/download", func(*fwcontext.Request) *fwcontext.Response {
				switch testCase.name {
				case "安全路径拒绝":
					return fwcontext.NewResponse().DownloadSafe(root, "../outside.txt", "download.txt")
				case "流式自定义状态":
					return fwcontext.NewResponse().Stream(func(writer io.Writer) error {
						writer.(http.ResponseWriter).WriteHeader(http.StatusAccepted)
						_, err := io.WriteString(writer, "accepted")
						return err
					})
				default:
					return fwcontext.NewResponse().Download(target, "download.txt")
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			endStatus, responseStatus, terminatedStatus := 0, 0, 0
			if err := app.Event().Listen(event.EventHttpEnd, &event.SimpleListener{Handler: func(current event.Event) error {
				ended := current.(*event.HttpEndEvent)
				endStatus = ended.StatusCode
				responseStatus = ended.Data.(*fwcontext.Response).GetStatus()
				return nil
			}}); err != nil {
				t.Fatal(err)
			}
			handler := newTestHTTPHandler(t, app)
			handler.middleware.PipeLifecycle(func(request *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
				return next(request)
			}, func(_ *fwcontext.Request, response *fwcontext.Response) {
				terminatedStatus = response.GetStatus()
			})
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/download", nil))
			if recorder.Code != testCase.want || endStatus != testCase.want || responseStatus != testCase.want || terminatedStatus != testCase.want {
				t.Fatalf("最终状态不一致: client=%d event=%d response=%d terminate=%d want=%d", recorder.Code, endStatus, responseStatus, terminatedStatus, testCase.want)
			}
		})
	}
}

func TestEndReportsStatusOfRegularSwitchingProtocolsResponse(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	if _, err := mustHTTPRoute(t, app).Get("/upgrade", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusSwitchingProtocols)
	})); err != nil {
		t.Fatal(err)
	}
	var ended *event.HttpEndEvent
	if err := app.Event().Listen(event.EventHttpEnd, &event.SimpleListener{Handler: func(current event.Event) error {
		ended = current.(*event.HttpEndEvent)
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	writer := httptest.NewRecorder()
	newTestHTTPHandler(t, app).ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/upgrade", nil))
	if writer.Code != http.StatusSwitchingProtocols || ended == nil || ended.StatusCode != writer.Code ||
		ended.Data.(*fwcontext.Response).GetStatus() != writer.Code {
		t.Fatalf("直接写出的 101 必须进入结束事件: wire=%d event=%+v", writer.Code, ended)
	}
}
