package http

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	framework "github.com/zhuhanxin0308/thinkgo/v3"
	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/event"
	"github.com/zhuhanxin0308/thinkgo/v3/log"
)

func TestHijackedResponseObservesOnlyDeclaredWireStatus(t *testing.T) {
	for _, test := range []struct {
		name                 string
		wireStatus, declared int
	}{
		{name: "状态未知", wireStatus: http.StatusSwitchingProtocols},
		{name: "声明升级状态", wireStatus: http.StatusSwitchingProtocols, declared: http.StatusSwitchingProtocols},
		{name: "声明普通状态", wireStatus: http.StatusForbidden, declared: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": true, "min_size": 1})
			registry := mustHTTPMetrics(t, app)
			registry.Enable()
			driver := &accessLogDriver{}
			logger := log.NewLog(driver)
			t.Cleanup(func() { _ = logger.Close() })
			if err := app.Instance(string(framework.ServiceLog), logger); err != nil {
				t.Fatal(err)
			}
			var ended *event.HttpEndEvent
			if err := app.Event().Listen(event.EventHttpEnd, &event.SimpleListener{Handler: func(current event.Event) error {
				ended = current.(*event.HttpEndEvent)
				return nil
			}}); err != nil {
				t.Fatal(err)
			}
			if _, err := mustHTTPRoute(t, app).Get("/upgrade", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if err := RecordHijackedStatus(w, http.StatusSwitchingProtocols); !errors.Is(err, ErrResponseBoundaryCrossed) {
					t.Errorf("接管前不得声明状态: %v", err)
				}
				conn, rw, err := http.NewResponseController(w).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.Close()
				headers := "Content-Length: 0\r\n"
				if test.wireStatus == http.StatusSwitchingProtocols {
					headers = "Connection: Upgrade\r\nUpgrade: test\r\n"
				}
				if _, err := fmt.Fprintf(rw, "HTTP/1.1 %d %s\r\n%s\r\n", test.wireStatus, http.StatusText(test.wireStatus), headers); err != nil {
					t.Error(err)
					return
				}
				if err := rw.Flush(); err != nil {
					t.Error(err)
					return
				}
				if test.declared == 0 {
					return
				}
				for range 2 {
					if err := RecordHijackedStatus(w, test.declared); err != nil {
						t.Error(err)
					}
				}
				if err := RecordHijackedStatus(w, http.StatusContinue); !errors.Is(err, fwcontext.ErrInvalidResponseStatus) {
					t.Errorf("不得声明临时状态: %v", err)
				}
				if err := RecordHijackedStatus(w, http.StatusAccepted); !errors.Is(err, ErrResponseBoundaryCrossed) {
					t.Errorf("已声明的状态不得覆盖: %v", err)
				}
			})); err != nil {
				t.Fatal(err)
			}
			host := newTestHTTPHandler(t, app)
			server, client := net.Pipe()
			defer client.Close()
			defer server.Close()
			if err := client.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			if err := server.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			raw := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/upgrade", nil)
			raw.Header.Set("Accept-Encoding", "gzip")
			target := &hijackCapabilityWriter{responseProtocolRecorder: &responseProtocolRecorder{}, connection: server}
			served := make(chan struct{})
			go func() {
				defer close(served)
				host.ServeHTTP(target, raw)
			}()
			wire, err := http.ReadResponse(bufio.NewReader(client), raw)
			<-served
			if err != nil || wire.StatusCode != test.wireStatus || len(target.statuses) != 0 {
				t.Fatalf("接管后的线路状态或所有权错误: %v, %v", wire, err)
			}
			_ = wire.Body.Close()
			if ended == nil || !ended.Hijacked() || ended.StatusCode != test.declared ||
				ended.Data.(*fwcontext.Response).GetStatus() != test.declared {
				t.Fatalf("HttpEnd 未正确反映声明和接管: %+v", ended)
			}
			snapshot := registry.Snapshot()
			if snapshot.Requests != 1 || snapshot.HijackedKnown+snapshot.HijackedUnknown != 1 ||
				(snapshot.HijackedUnknown == 1) != (test.declared == 0) || snapshot.StatusClasses[1] != 0 {
				t.Fatalf("接管被错误计入普通响应: %+v", snapshot)
			}
			if err := logger.Flush(t.Context()); err != nil {
				t.Fatal(err)
			}
			statusText := strconv.Itoa(test.declared)
			if test.declared == 0 {
				statusText = "hijacked/unknown"
			}
			for _, entry := range driver.allEntries() {
				if strings.Contains(entry.Message, "GET /upgrade "+statusText+" ") && entry.Context["hijacked"] == true {
					return
				}
			}
			t.Fatal("访问日志缺少接管状态")
		})
	}
}
