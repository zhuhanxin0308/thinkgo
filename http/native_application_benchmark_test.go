package http

import (
	"bytes"
	"encoding/json"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework"
	fwcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
	frameworklog "github.com/zhuhanxin0308/thinkgo/framework/log"
	"github.com/zhuhanxin0308/thinkgo/framework/route"
)

const (
	benchmarkHTTPBody     = "ok"
	benchmarkHTTPJSONBody = `{"name":"thinkgo"}`

	// 当前 Windows/Go 工具链实测分别为 77 和 89 allocs/op；上限仅保留
	// 小幅工具链波动空间，同时保证显著分配回归会阻断发布。
	singleHealthAllocationBudget = 90
	multiHealthAllocationBudget  = 105
)

func BenchmarkNativeHTTP(b *testing.B) {
	b.Run("Health", func(b *testing.B) {
		benchmarkNativeHTTPScenario(b, stdhttp.MethodGet, "http://example.com/health", "")
	})
	b.Run("JSONBody", func(b *testing.B) {
		benchmarkNativeHTTPScenario(b, stdhttp.MethodPost, "http://example.com/health", benchmarkHTTPJSONBody)
	})
}

func BenchmarkSingleAppHTTP(b *testing.B) {
	b.Run("Health", func(b *testing.B) {
		benchmarkSingleHTTPScenario(b, stdhttp.MethodGet, "http://example.com/health", "")
	})
	b.Run("JSONBody", func(b *testing.B) {
		benchmarkSingleHTTPScenario(b, stdhttp.MethodPost, "http://example.com/health", benchmarkHTTPJSONBody)
	})
}

func BenchmarkMultiAppHTTP(b *testing.B) {
	b.Run("Health", func(b *testing.B) {
		benchmarkMultiHTTPScenario(b, stdhttp.MethodGet, "http://example.com/index/health", "")
	})
	b.Run("JSONBody", func(b *testing.B) {
		benchmarkMultiHTTPScenario(b, stdhttp.MethodPost, "http://example.com/index/health", benchmarkHTTPJSONBody)
	})
}

// TestHTTPHealthAllocationBudgets 把单应用与原生多应用分配上限纳入普通测试，
// 确保 CI 的性能门禁确实执行并能阻止明显回归。
func TestHTTPHealthAllocationBudgets(t *testing.T) {
	singleHandler := newBenchmarkSingleHTTPHandler(t)
	assertHTTPHealthAllocationBudget(t, singleHandler, benchmarkHTTPRequestFactory(stdhttp.MethodGet, "http://example.com/health", ""), singleHealthAllocationBudget)

	multiHandler := newBenchmarkMultiHTTPHandler(t)
	assertHTTPHealthAllocationBudget(t, multiHandler, benchmarkHTTPRequestFactory(stdhttp.MethodGet, "http://example.com/index/health", ""), multiHealthAllocationBudget)
}

func assertHTTPHealthAllocationBudget(t *testing.T, handler stdhttp.Handler, requestFactory func() *stdhttp.Request, budget float64) {
	t.Helper()
	warmupHTTPHandler(t, handler, requestFactory())
	allocations := testing.AllocsPerRun(100, func() {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, requestFactory())
		assertBenchmarkHTTPResponse(t, recorder)
	})
	if allocations > budget {
		t.Fatalf("HTTP 健康请求分配超出预算: got=%.2f budget=%.2f", allocations, budget)
	}
}

func newBenchmarkSingleHTTPHandler(t testing.TB) *Http {
	t.Helper()
	application := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	setBenchmarkHTTPLogLevel(t, application)
	registerBenchmarkHTTPRoute(t, application)
	handler, err := NewHttp(application)
	if err != nil {
		t.Fatalf("创建单应用 HTTP 基准处理器失败: %v", err)
	}
	return handler
}

func newBenchmarkMultiHTTPHandler(t testing.TB) *Http {
	t.Helper()
	basePath := t.TempDir()
	ensureHTTPTestConfigFiles(t, basePath)
	for _, name := range []string{"index", "admin"} {
		if err := os.MkdirAll(filepath.Join(basePath, "app", name), 0o755); err != nil {
			t.Fatalf("创建 HTTP 基准应用目录 %q 失败: %v", name, err)
		}
	}
	application := framework.NewConsoleAppUninitialized(basePath)
	t.Cleanup(func() { _ = application.Close() })
	loader := func(current *framework.App) error {
		return current.RegisterRouteLoader(func(routeApplication *framework.App) error {
			routeApplication.Route().Any("/health", benchmarkHTTPRouteHandler)
			return nil
		})
	}
	if err := application.RegisterApplications(
		func(*framework.App) error { return nil },
		framework.ApplicationDefinition{Name: "index", Register: loader},
		framework.ApplicationDefinition{Name: "admin", Register: loader},
	); err != nil {
		t.Fatalf("注册 HTTP 基准多应用失败: %v", err)
	}
	handler, err := NewHttp(application)
	if err != nil {
		t.Fatalf("创建原生多应用 HTTP 基准处理器失败: %v", err)
	}
	if err := handler.ensureInitialized(); err != nil {
		t.Fatalf("初始化原生多应用 HTTP 基准处理器失败: %v", err)
	}
	for _, current := range handler.applicationHost.applications {
		setBenchmarkHTTPLogLevel(t, current.app)
	}
	return handler
}

func setBenchmarkHTTPLogLevel(t testing.TB, application *framework.App) {
	t.Helper()
	logger, err := framework.ResolveServiceAs[*frameworklog.Log](application, framework.ServiceLog)
	if err != nil {
		t.Fatalf("解析 HTTP 基准日志服务失败: %v", err)
	}
	logger.SetLevels([]string{frameworklog.LevelError})
}

func registerBenchmarkHTTPRoute(t testing.TB, application *framework.App) {
	t.Helper()
	router, err := framework.ResolveServiceAs[*route.Router](application, framework.ServiceRoute)
	if err != nil {
		t.Fatalf("解析 HTTP 基准路由服务失败: %v", err)
	}
	if _, err := router.Any("/health", benchmarkHTTPRouteHandler); err != nil {
		t.Fatalf("注册 HTTP 基准路由失败: %v", err)
	}
}

func benchmarkHTTPRouteHandler(request *fwcontext.Request) *fwcontext.Response {
	if _, err := request.Body(); err != nil {
		return fwcontext.NewResponse().Code(stdhttp.StatusInternalServerError).Content(stdhttp.StatusText(stdhttp.StatusInternalServerError))
	}
	return fwcontext.NewResponse().Content(benchmarkHTTPBody)
}

func warmupHTTPHandler(t testing.TB, handler stdhttp.Handler, request *stdhttp.Request) {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	assertBenchmarkHTTPResponse(t, recorder)
}

func benchmarkNativeHTTPScenario(b *testing.B, method, target, body string) {
	handler := stdhttp.HandlerFunc(func(writer stdhttp.ResponseWriter, request *stdhttp.Request) {
		if body != "" {
			payload, err := io.ReadAll(request.Body)
			if err != nil {
				stdhttp.Error(writer, stdhttp.StatusText(stdhttp.StatusBadRequest), stdhttp.StatusBadRequest)
				return
			}
			var decoded map[string]interface{}
			if err = json.Unmarshal(payload, &decoded); err != nil {
				stdhttp.Error(writer, stdhttp.StatusText(stdhttp.StatusBadRequest), stdhttp.StatusBadRequest)
				return
			}
		}
		writer.WriteHeader(stdhttp.StatusOK)
		_, _ = writer.Write([]byte(benchmarkHTTPBody))
	})
	benchmarkHTTPScenario(b, handler, benchmarkHTTPRequestFactory(method, target, body))
}

func benchmarkSingleHTTPScenario(b *testing.B, method, target, body string) {
	benchmarkHTTPScenario(b, newBenchmarkSingleHTTPHandler(b), benchmarkHTTPRequestFactory(method, target, body))
}

func benchmarkMultiHTTPScenario(b *testing.B, method, target, body string) {
	benchmarkHTTPScenario(b, newBenchmarkMultiHTTPHandler(b), benchmarkHTTPRequestFactory(method, target, body))
}

func benchmarkHTTPScenario(b *testing.B, handler stdhttp.Handler, requestFactory func() *stdhttp.Request) {
	warmupHTTPHandler(b, handler, requestFactory())
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, requestFactory())
		assertBenchmarkHTTPResponse(b, recorder)
	}
}

func newBenchmarkHTTPRequest(method, target, body string) *stdhttp.Request {
	var reader io.Reader
	if body != "" {
		reader = bytes.NewReader([]byte(body))
	}
	request := httptest.NewRequest(method, target, reader)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}

func benchmarkHTTPRequestFactory(method, target, body string) func() *stdhttp.Request {
	if body == "" {
		request := newBenchmarkHTTPRequest(method, target, body)
		return func() *stdhttp.Request { return request }
	}
	return func() *stdhttp.Request { return newBenchmarkHTTPRequest(method, target, body) }
}

func assertBenchmarkHTTPResponse(t testing.TB, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if recorder.Code != stdhttp.StatusOK || recorder.Body.String() != benchmarkHTTPBody {
		t.Fatalf("HTTP 基准响应异常: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}
