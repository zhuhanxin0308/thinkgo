package http

import (
	stdcontext "context"
	"errors"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	framework "github.com/zhuhanxin0308/thinkgo/v3"
	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/exception"
	frameworklog "github.com/zhuhanxin0308/thinkgo/v3/log"
)

const (
	disabledHTTPLogConfiguration = `{"close":true,"level":[],"channels":{}}`
	filteredHTTPLogConfiguration = `{"close":false,"level":["error"],"default":"file","channels":{"file":{"type":"file","path":"runtime/log"}}}`
	disabledLogAllocationSamples = 100
)

// newConfiguredHTTPLogHandler 在 Provider 初始化之前写入真实日志配置，避免只修改已初始化的配置快照。
func newConfiguredHTTPLogHandler(t *testing.T, configuration string) (*framework.App, *Http) {
	t.Helper()
	basePath := t.TempDir()
	ensureHTTPTestConfigFiles(t, basePath)
	if err := os.WriteFile(filepath.Join(basePath, "config", "log.json"), []byte(configuration), 0o600); err != nil {
		t.Fatalf("写入日志关闭配置失败: %v", err)
	}
	app := framework.NewConsoleAppUninitialized(basePath)
	t.Cleanup(func() {
		if err := app.Close(); err != nil {
			t.Errorf("关闭日志配置测试应用失败: %v", err)
		}
	})
	handler, err := NewHttp(app)
	if err != nil {
		t.Fatalf("创建日志配置测试 HTTP 内核失败: %v", err)
	}
	return app, handler
}

// TestHTTPClosedLogUsesFinalServiceSnapshot 验证首次真实请求拿到 Provider 发布的禁用实例，且请求 DI 与异常处理器保持同一身份。
func TestHTTPClosedLogUsesFinalServiceSnapshot(t *testing.T) {
	app, handler := newConfiguredHTTPLogHandler(t, disabledHTTPLogConfiguration)
	original := app.Log()
	var requestLogger any
	if err := app.RegisterRouteLoader(func(current *framework.App) error {
		current.Route().Get("/closed-log", func(request *fwcontext.Request) *fwcontext.Response {
			var resolveErr error
			requestLogger, resolveErr = request.Make(string(framework.ServiceLog))
			if resolveErr != nil {
				t.Errorf("请求作用域解析日志失败: %v", resolveErr)
			}
			return fwcontext.NewResponse().Code(stdhttp.StatusCreated).Content("created")
		})
		return nil
	}); err != nil {
		t.Fatalf("注册日志身份回归路由失败: %v", err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(stdhttp.MethodGet, "http://example.com/closed-log", nil))
	if recorder.Code != stdhttp.StatusCreated || recorder.Body.String() != "created" {
		t.Fatalf("关闭日志不应改变业务响应: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	finalLogger, err := framework.ResolveServiceAs[*frameworklog.Log](app, framework.ServiceLog)
	if err != nil {
		t.Fatalf("解析最终日志服务失败: %v", err)
	}
	if finalLogger == original || finalLogger != app.Log() || handler.log != finalLogger || requestLogger != finalLogger {
		t.Fatalf("日志身份没有一致刷新: original=%p final=%p facade=%p http=%p request=%p", original, finalLogger, app.Log(), handler.log, requestLogger)
	}
	defaultHandler, ok := handler.exception.(*exception.Handle)
	if !ok || defaultHandler.Log != finalLogger {
		t.Fatalf("默认异常处理器未使用最终日志实例: %#v", handler.exception)
	}
	for _, level := range []string{frameworklog.LevelInfo, frameworklog.LevelError, frameworklog.LevelWarning, frameworklog.LevelSQL} {
		if finalLogger.IsLevelEnabled(level) {
			t.Errorf("log.close=true 时最终 HTTP 日志器仍开启 %q", level)
		}
	}
	if err := original.Flush(stdcontext.Background()); !errors.Is(err, frameworklog.ErrLogClosed) {
		t.Fatalf("被替换的初始日志器应已退役，实际为 %v", err)
	}
}

// TestHTTPClosedLogRemainsApplicationLocal 验证根应用关闭日志时，独立子应用仍按自己的配置开启并发布正确实例。
func TestHTTPClosedLogRemainsApplicationLocal(t *testing.T) {
	basePath := t.TempDir()
	writeNativeHTTPConfig(t, basePath, `{"app_env":"test","default_app":"index","server":{"host":"127.0.0.1","port":8080},"compression":{"enable":false}}`)
	if err := os.WriteFile(filepath.Join(basePath, "config", "log.json"), []byte(disabledHTTPLogConfiguration), 0o600); err != nil {
		t.Fatalf("写入根应用日志配置失败: %v", err)
	}
	adminConfigPath := filepath.Join(basePath, "app", "admin", "config")
	if err := os.MkdirAll(adminConfigPath, 0o700); err != nil {
		t.Fatalf("创建子应用配置目录失败: %v", err)
	}
	adminConfiguration := `{"close":false,"level":[],"default":"file","channels":{"file":{"type":"file","path":"runtime/admin-log"}}}`
	if err := os.WriteFile(filepath.Join(adminConfigPath, "log.json"), []byte(adminConfiguration), 0o600); err != nil {
		t.Fatalf("写入子应用日志配置失败: %v", err)
	}
	app := framework.NewConsoleAppUninitialized(basePath)
	t.Cleanup(func() {
		if err := app.Close(); err != nil {
			t.Errorf("关闭多应用日志测试失败: %v", err)
		}
	})
	if err := app.RegisterApplications(
		nativeHTTPGlobalLoader,
		framework.ApplicationDefinition{Name: "index", Register: nativeHTTPApplicationLoader("index")},
		framework.ApplicationDefinition{Name: "admin", Register: nativeHTTPApplicationLoader("admin")},
	); err != nil {
		t.Fatalf("注册多应用日志测试失败: %v", err)
	}
	handler := newTestHTTPHandler(t, app)
	for _, name := range []string{"index", "admin"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(stdhttp.MethodGet, "http://example.com/"+name+"/who", nil))
		if recorder.Code != stdhttp.StatusOK || !strings.HasPrefix(recorder.Body.String(), name+"|") {
			t.Fatalf("多应用日志配置改变了请求路由: application=%s status=%d body=%q", name, recorder.Code, recorder.Body.String())
		}
	}
	if handler.applicationHost == nil {
		t.Fatal("真实多应用请求未创建 HTTP 宿主")
	}
	indexHandler := handler.applicationHost.applications["index"]
	adminHandler := handler.applicationHost.applications["admin"]
	if indexHandler == nil || adminHandler == nil || indexHandler.log == adminHandler.log {
		t.Fatal("根应用和独立子应用必须保持独立日志身份")
	}
	if handler.log != app.Log() || indexHandler.log != app.Log() || adminHandler.log != adminHandler.app.Log() {
		t.Fatal("多应用 HTTP 处理器未发布各自最终日志服务")
	}
	if indexHandler.log.IsLevelEnabled(frameworklog.LevelInfo) || indexHandler.log.IsLevelEnabled(frameworklog.LevelError) {
		t.Error("主应用没有应用根配置中的日志关闭状态")
	}
	if !adminHandler.log.IsLevelEnabled(frameworklog.LevelInfo) || !adminHandler.log.IsLevelEnabled(frameworklog.LevelError) {
		t.Error("子应用显式开启日志后不应继承主应用的禁用状态")
	}
}

// TestHTTPDisabledAccessLogDoesNotAllocate 使用完整请求状态验证关闭和级别过滤均发生在访问字段构造之前。
func TestHTTPDisabledAccessLogDoesNotAllocate(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		configuration string
	}{
		{name: "关闭日志", configuration: disabledHTTPLogConfiguration},
		{name: "过滤访问级别", configuration: filteredHTTPLogConfiguration},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, handler := newConfiguredHTTPLogHandler(t, testCase.configuration)
			if err := handler.ensureInitialized(); err != nil {
				t.Fatalf("初始化访问日志回归内核失败: %v", err)
			}
			state := newDisabledLogRequestState(t)
			for _, nonBlocking := range []bool{false, true} {
				name := "有界入队"
				if nonBlocking {
					name = "非阻塞入队"
				}
				t.Run(name, func(t *testing.T) {
					allocations := testing.AllocsPerRun(disabledLogAllocationSamples, func() {
						if err := handler.writeAccessLogRecord(stdcontext.Background(), state, nonBlocking); err != nil {
							t.Fatalf("禁用访问日志不应返回入队错误: %v", err)
						}
					})
					if allocations != 0 {
						t.Errorf("禁用访问日志不应构造字段或序列化，实际每次 %.2f 次分配", allocations)
					}
				})
			}
			if dropped := handler.log.DroppedEntryCount(); dropped != 0 {
				t.Errorf("禁用日志不应计为队列丢弃，实际为 %d", dropped)
			}
		})
	}
}

func newDisabledLogRequestState(t *testing.T) *requestServeState {
	t.Helper()
	raw := httptest.NewRequest(stdhttp.MethodGet, "http://example.com/orders/42?token=private", nil)
	raw.Header.Set("User-Agent", "disabled-log-regression")
	raw.RemoteAddr = "192.0.2.1:12345"
	request, err := fwcontext.NewRequest(raw)
	if err != nil {
		t.Fatalf("创建日志测试请求失败: %v", err)
	}
	t.Cleanup(func() {
		if err := request.Cleanup(); err != nil {
			t.Errorf("清理日志测试请求失败: %v", err)
		}
	})
	statusWriter := newStatusTrackingResponseWriter(httptest.NewRecorder())
	statusWriter.WriteHeader(stdhttp.StatusAccepted)
	return &requestServeState{raw: raw, req: request, statusWriter: statusWriter, startedAt: time.Now()}
}

type disabledLogCountingError struct {
	calls atomic.Int32
}

func (failure *disabledLogCountingError) Error() string {
	failure.calls.Add(1)
	return "request failed password=abcdefgh"
}

// TestHTTPDisabledErrorsDoNotEvaluateFields 覆盖普通、有界和非阻塞三个错误日志入口，防止先求值再过滤。
func TestHTTPDisabledErrorsDoNotEvaluateFields(t *testing.T) {
	for _, configuration := range []struct {
		name string
		json string
	}{
		{name: "关闭日志", json: disabledHTTPLogConfiguration},
		{name: "过滤错误级别", json: `{"level":["info"],"default":"file","channels":{"file":{"type":"file","path":"runtime/log"}}}`},
	} {
		t.Run(configuration.name, func(t *testing.T) {
			_, handler := newConfiguredHTTPLogHandler(t, configuration.json)
			if err := handler.ensureInitialized(); err != nil {
				t.Fatalf("初始化错误日志回归内核失败: %v", err)
			}
			for _, entrypoint := range httpErrorLogEntrypoints(handler) {
				t.Run(entrypoint.name, func(t *testing.T) {
					failure := &disabledLogCountingError{}
					if err := entrypoint.record(failure); err != nil {
						t.Fatalf("禁用错误日志不应返回错误: %v", err)
					}
					if calls := failure.calls.Load(); calls != 0 {
						t.Errorf("禁用错误日志不应求值 Error，实际调用 %d 次", calls)
					}
				})
			}
		})
	}
}

// TestHTTPEnabledErrorsKeepRedactedOutput 验证开启路径仍求值并输出脱敏错误，避免通过无条件返回修复关闭路径。
func TestHTTPEnabledErrorsKeepRedactedOutput(t *testing.T) {
	driver := &accessLogDriver{}
	logger := frameworklog.NewLog(driver)
	logger.SetLevels([]string{frameworklog.LevelError})
	t.Cleanup(func() { _ = logger.Close() })
	handler := &Http{log: logger}
	entrypoints := httpErrorLogEntrypoints(handler)
	for _, entrypoint := range entrypoints {
		failure := &disabledLogCountingError{}
		if err := entrypoint.record(failure); err != nil {
			t.Fatalf("开启的 %s 错误日志失败: %v", entrypoint.name, err)
		}
		if calls := failure.calls.Load(); calls != 1 {
			t.Errorf("开启的 %s 应求值一次，实际为 %d", entrypoint.name, calls)
		}
	}
	if err := logger.Flush(stdcontext.Background()); err != nil {
		t.Fatalf("刷出开启错误日志失败: %v", err)
	}
	entries := driver.allEntries()
	if len(entries) != len(entrypoints) {
		t.Fatalf("错误日志条目数量错误: got=%d want=%d", len(entries), len(entrypoints))
	}
	for _, entry := range entries {
		if entry.Level != frameworklog.LevelError || entry.Message != "错误日志回归" || entry.Context["error"] != "request failed password=abcd[REDACTED]" {
			t.Errorf("开启错误日志未保留级别、消息或脱敏内容: %#v", entry)
		}
		if strings.Contains(entry.FormatEntry(), "abcdefgh") {
			t.Errorf("开启错误日志泄露完整凭据: %q", entry.FormatEntry())
		}
	}
}

func httpErrorLogEntrypoints(handler *Http) []struct {
	name   string
	record func(error) error
} {
	return []struct {
		name   string
		record func(error) error
	}{
		{name: "普通入队", record: func(err error) error { handler.logHTTPError("错误日志回归", err); return nil }},
		{name: "有界入队", record: func(err error) error {
			return handler.logHTTPErrorContext(stdcontext.Background(), "错误日志回归", err)
		}},
		{name: "非阻塞入队", record: func(err error) error { return handler.logHTTPErrorNonBlocking("错误日志回归", err) }},
	}
}
