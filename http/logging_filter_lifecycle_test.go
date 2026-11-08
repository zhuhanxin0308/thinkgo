package http

import (
	"bytes"
	"context"
	stdhttp "net/http"
	"strings"
	"testing"

	framework "github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/event"
	frameworklog "github.com/zhuhanxin0308/thinkgo/v3/log"
)

// TestFilteredWarningsDoNotConsumeDelivery 验证被级别过滤的告警不会占用一次性通知机会。
func TestFilteredWarningsDoNotConsumeDelivery(t *testing.T) {
	for _, kind := range []string{"应用事件监听器", "生产限流配置"} {
		t.Run(kind, func(t *testing.T) {
			driver := &accessLogDriver{}
			logger := frameworklog.NewLog(driver)
			logger.SetLevels([]string{frameworklog.LevelError})
			t.Cleanup(func() { _ = logger.Close() })
			handler := &Http{log: logger}
			var warn func()
			switch kind {
			case "应用事件监听器":
				dispatcher := event.NewDispatcher()
				listener := &event.SimpleListener{Handler: func(event.Event) error { return nil }}
				if err := dispatcher.ListenApplicationEvents(map[string][]event.Listener{
					event.EventHttpRun: {listener},
				}); err != nil {
					t.Fatal(err)
				}
				handler.event = dispatcher
				warn = handler.warnApplicationRunListeners
			case "生产限流配置":
				handler.app = newRateLimitWarningApp(t, "production", framework.SecurityProfileStatelessAPI, nil)
				warn = handler.warnProductionRateLimit
			}
			warn()
			if err := logger.Flush(context.Background()); err != nil {
				t.Fatal(err)
			}
			if entries := driver.allEntries(); len(entries) != 0 {
				t.Fatalf("被过滤的告警不应输出，实际为 %d 条", len(entries))
			}
			logger.SetLevels([]string{frameworklog.LevelWarning})
			warn()
			warn()
			if err := logger.Flush(context.Background()); err != nil {
				t.Fatal(err)
			}
			entries := driver.allEntries()
			if len(entries) != 1 || entries[0].Level != frameworklog.LevelWarning {
				t.Fatalf("恢复告警级别后应恰好输出一次，实际为 %#v", entries)
			}
		})
	}
}

// TestFilteredDispatchLogPreservesResponse 验证日志过滤不求值生产错误，调试响应仍按原契约生成。
func TestFilteredDispatchLogPreservesResponse(t *testing.T) {
	for _, debug := range []bool{false, true} {
		t.Run(map[bool]string{false: "生产响应", true: "调试响应"}[debug], func(t *testing.T) {
			logger := frameworklog.NewLog()
			logger.SetLevels([]string{frameworklog.LevelInfo})
			t.Cleanup(func() { _ = logger.Close() })
			handler := &Http{app: &framework.App{DebugMode: debug}, log: logger}
			failure := &disabledLogCountingError{}
			response := handler.dispatchInternalError("Example@Action", failure)
			wantCalls := int32(0)
			wantBody := stdhttp.StatusText(stdhttp.StatusInternalServerError)
			if debug {
				wantCalls = 1
				wantBody = "request failed password=abcdefgh"
			}
			if failure.calls.Load() != wantCalls {
				t.Errorf("仅响应所需的错误转换可以执行：实际 %d 次，期望 %d 次", failure.calls.Load(), wantCalls)
			}
			if response.GetStatus() != stdhttp.StatusInternalServerError || string(response.GetBody()) != wantBody {
				t.Fatalf("日志过滤改变了错误响应：status=%d body=%q", response.GetStatus(), response.GetBody())
			}
		})
	}
}

// TestFilteredServerLogKeepsListeningAddress 验证服务地址输出不依赖日志级别，同时跳过日志专用字段分配。
func TestFilteredServerLogKeepsListeningAddress(t *testing.T) {
	logger := frameworklog.NewLog()
	logger.SetLevels([]string{frameworklog.LevelError})
	t.Cleanup(func() { _ = logger.Close() })
	handler := &Http{log: logger}
	address := "127.0.0.1:18080"
	allocations := testing.AllocsPerRun(disabledLogAllocationSamples, func() {
		handler.logServerStarted(address)
	})
	if allocations != 0 {
		t.Errorf("过滤启动日志后不应构造字段，实际每次 %.2f 次分配", allocations)
	}
	var output bytes.Buffer
	handler.startupOutput = &output
	handler.logServerStarted(address)
	if !strings.Contains(output.String(), "http://"+address) {
		t.Fatalf("关闭启动日志不应隐藏实际监听地址：%q", output.String())
	}
}
