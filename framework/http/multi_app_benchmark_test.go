package http

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"thinkgo/framework"
	fwcontext "thinkgo/framework/context"
	"thinkgo/framework/log"
	"thinkgo/framework/route"
)

const benchmarkHTTPBody = "ok"
const benchmarkHTTPJSONBody = `{"name":"thinkgo"}`

const (
	// 当前 Windows/Go 工具链的单应用基线为 41 allocs/op，48 为保留小幅回归空间的上限。
	singleHealthAllocationBudget = 48
	multiHealthAllocationBudget  = 48
)

func BenchmarkNativeHTTP(b *testing.B) {
	b.Run("Health", func(b *testing.B) {
		benchmarkNativeHTTPScenario(b, http.MethodGet, "http://example.com/health", "")
	})
	b.Run("JSONBody", func(b *testing.B) {
		benchmarkNativeHTTPScenario(b, http.MethodPost, "http://example.com/health", benchmarkHTTPJSONBody)
	})
}

func BenchmarkSingleAppHTTP(b *testing.B) {
	b.Run("Health", func(b *testing.B) {
		benchmarkSingleHTTPScenario(b, http.MethodGet, "http://example.com/health", "")
	})
	b.Run("JSONBody", func(b *testing.B) {
		benchmarkSingleHTTPScenario(b, http.MethodPost, "http://example.com/health", benchmarkHTTPJSONBody)
	})
}

func BenchmarkMultiAppHTTP(b *testing.B) {
	b.Run("Health", func(b *testing.B) {
		benchmarkMultiHTTPScenario(b, http.MethodGet, "http://example.com/index/health", "")
	})
	b.Run("JSONBody", func(b *testing.B) {
		benchmarkMultiHTTPScenario(b, http.MethodPost, "http://example.com/index/health", benchmarkHTTPJSONBody)
	})
}

// TestHTTPHealthAllocationBudgets 将稳定健康请求的分配上限纳入普通单测，避免只记录基准结果而不阻止回归。
func TestHTTPHealthAllocationBudgets(t *testing.T) {
	singleApp := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	mustHTTPLog(t, singleApp).SetLevels([]string{log.LevelError})
	registerBenchmarkHTTPRoute(t, singleApp)
	singleHandler, err := NewHttp(singleApp)
	if err != nil {
		t.Fatalf("创建单应用分配预算处理器失败: %v", err)
	}
	assertHTTPHealthAllocationBudget(t, singleHandler, benchmarkHTTPRequestFactory(http.MethodGet, "http://example.com/health", ""), singleHealthAllocationBudget)

	multiManager := newBenchmarkMultiHTTPManager(t)
	multiHandler, err := NewMultiHttp(multiManager)
	if err != nil {
		t.Fatalf("创建多应用分配预算处理器失败: %v", err)
	}
	assertHTTPHealthAllocationBudget(t, multiHandler, benchmarkHTTPRequestFactory(http.MethodGet, "http://example.com/index/health", ""), multiHealthAllocationBudget)
}

func assertHTTPHealthAllocationBudget(t *testing.T, handler http.Handler, requestFactory func() *http.Request, budget float64) {
	t.Helper()
	warmupHTTPHandler(t, handler, requestFactory())
	allocations := testing.AllocsPerRun(100, func() {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, requestFactory())
		if recorder.Code != http.StatusOK || recorder.Body.String() != benchmarkHTTPBody {
			t.Fatalf("分配预算探针响应异常: status=%d body=%q", recorder.Code, recorder.Body.String())
		}
	})
	if allocations > budget {
		t.Fatalf("HTTP 健康请求分配超出预算: got=%.2f budget=%.2f", allocations, budget)
	}
}

func registerBenchmarkHTTPRoute(b testing.TB, app *framework.App) {
	b.Helper()
	router, err := framework.ResolveServiceAs[*route.Router](app, framework.ServiceRoute)
	if err != nil {
		b.Fatalf("解析 HTTP 基准路由服务失败: %v", err)
	}
	if _, err := router.Any("/health", benchmarkHTTPRouteHandler); err != nil {
		b.Fatalf("注册 HTTP 基准路由失败: %v", err)
	}
}

func benchmarkHTTPRouteHandler(request *fwcontext.Request) *fwcontext.Response {
	if _, err := request.Body(); err != nil {
		return fwcontext.NewResponse().Code(http.StatusInternalServerError).Content(http.StatusText(http.StatusInternalServerError))
	}
	return fwcontext.NewResponse().Content(benchmarkHTTPBody)
}

func newBenchmarkMultiHTTPManager(b testing.TB) *framework.ApplicationManager {
	b.Helper()
	definitions := []framework.ApplicationDefinition{{
		Name: "index",
		Path: "app/index",
		Register: func(app *framework.App) error {
			return app.RegisterRouteLoader(func(app *framework.App) error {
				router, resolveErr := framework.ResolveServiceAs[*route.Router](app, framework.ServiceRoute)
				if resolveErr != nil {
					return resolveErr
				}
				_, err := router.Any("/health", benchmarkHTTPRouteHandler)
				return err
			})
		},
	}}
	manager := newMultiHTTPTestManagerWithDefinitions(b, nil, definitions)
	for _, app := range manager.Applications() {
		logger, err := framework.ResolveServiceAs[*log.Log](app, framework.ServiceLog)
		if err != nil {
			b.Fatalf("解析 HTTP 基准日志服务失败: %v", err)
		}
		logger.SetLevels([]string{log.LevelError})
	}
	return manager
}

func warmupHTTPHandler(b testing.TB, handler http.Handler, request *http.Request) {
	b.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	assertBenchmarkHTTPResponse(b, recorder)
}

func benchmarkNativeHTTPScenario(b *testing.B, method, target, body string) {
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if body != "" {
			payload, err := io.ReadAll(request.Body)
			if err != nil {
				http.Error(writer, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
				return
			}
			var decoded map[string]interface{}
			if err = json.Unmarshal(payload, &decoded); err != nil {
				http.Error(writer, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
				return
			}
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(benchmarkHTTPBody))
	})
	benchmarkHTTPScenario(b, handler, benchmarkHTTPRequestFactory(method, target, body))
}

func benchmarkSingleHTTPScenario(b *testing.B, method, target, body string) {
	app := newTestHTTPApp(b, b.TempDir(), map[string]interface{}{"enable": false})
	mustHTTPLog(b, app).SetLevels([]string{log.LevelError})
	registerBenchmarkHTTPRoute(b, app)
	handler, err := NewHttp(app)
	if err != nil {
		b.Fatalf("创建单应用 HTTP 基准处理器失败: %v", err)
	}
	benchmarkHTTPScenario(b, handler, benchmarkHTTPRequestFactory(method, target, body))
}

func benchmarkMultiHTTPScenario(b *testing.B, method, target, body string) {
	manager := newBenchmarkMultiHTTPManager(b)
	host, err := NewMultiHttp(manager)
	if err != nil {
		b.Fatalf("创建多应用 HTTP 基准处理器失败: %v", err)
	}
	benchmarkHTTPScenario(b, host, benchmarkHTTPRequestFactory(method, target, body))
}

func benchmarkHTTPScenario(b *testing.B, handler http.Handler, requestFactory func() *http.Request) {
	warmupHTTPHandler(b, handler, requestFactory())

	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, requestFactory())
		assertBenchmarkHTTPResponse(b, recorder)
	}
}

func newBenchmarkHTTPRequest(method, target, body string) *http.Request {
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

func benchmarkHTTPRequestFactory(method, target, body string) func() *http.Request {
	if body == "" {
		request := newBenchmarkHTTPRequest(method, target, body)
		return func() *http.Request { return request }
	}
	return func() *http.Request {
		return newBenchmarkHTTPRequest(method, target, body)
	}
}

func assertBenchmarkHTTPResponse(b testing.TB, recorder *httptest.ResponseRecorder) {
	b.Helper()
	if recorder.Code != http.StatusOK || recorder.Body.String() != benchmarkHTTPBody {
		b.Fatalf("HTTP 基准响应异常: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}
