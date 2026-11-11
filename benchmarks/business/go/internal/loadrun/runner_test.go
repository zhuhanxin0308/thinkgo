package loadrun

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// respondCatalog 仅作为本地测试端点，不替代正式业务应用。
func respondCatalog(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Content-Type", "application/json")
	category, err := strconv.Atoi(request.URL.Query().Get("category_id"))
	if err != nil {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	product := category
	for product%inactiveProductEvery == 0 {
		product += defaultCategories
	}
	fmt.Fprintf(writer, `{"items":[{"id":%d,"name":"商品","price_cents":100,"stock":10,"category":"分类-%d"}],"next_cursor":null}`, product, category)
}

func assertConservation(t *testing.T, report Report) {
	t.Helper()
	counters := report.Totals
	if counters.Planned != counters.Started+counters.Dropped || counters.Started != counters.Completed || counters.Completed != counters.Success+counters.StatusErrors+counters.TransportErrors+counters.SemanticErrors {
		t.Fatalf("请求计数不守恒: %#v", counters)
	}
	var planned, completed uint64
	for _, bucket := range report.Seconds {
		planned += bucket.Counts.Planned
		completed += bucket.Counts.Completed
	}
	if planned != counters.Planned || completed != counters.Completed {
		t.Fatalf("逐秒计数丢失: planned=%d completed=%d totals=%#v", planned, completed, counters)
	}
}

func TestOpenRunnerAccountsForOverload(t *testing.T) {
	var inflight, peak atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		active := inflight.Add(1)
		defer inflight.Add(-1)
		for current := peak.Load(); active > current && !peak.CompareAndSwap(current, active); current = peak.Load() {
		}
		time.Sleep(20 * time.Millisecond)
		respondCatalog(writer, request)
	}))
	defer server.Close()
	configuration := testConfig()
	configuration.URL = server.URL
	configuration.TenantCount = 1
	configuration.Scenario = "catalog"
	configuration.Rate = 1000
	configuration.Duration = 80 * time.Millisecond
	configuration.MaxInflight = 1
	report, err := Run(context.Background(), configuration)
	if err != nil {
		t.Fatal(err)
	}
	assertConservation(t, report)
	if report.Totals.Planned != 80 || report.Totals.Dropped == 0 || report.Totals.Success == 0 || peak.Load() > 1 {
		t.Fatalf("开放过载被隐藏或未限制在途: %#v peak=%d", report.Totals, peak.Load())
	}
	if report.Latency.Count != report.Totals.Completed || report.Latency.P99MS < 15 || report.HTTPDuration.Count != report.Totals.Completed {
		t.Fatalf("延迟直方图错误: %#v", report.Latency)
	}
}

func TestOpenWindowExcludesBoundaryArrival(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(respondCatalog))
	defer server.Close()
	configuration := testConfig()
	configuration.URL, configuration.Scenario, configuration.TenantCount = server.URL, "catalog", 1
	configuration.Duration, configuration.Rate = 70*time.Millisecond, 100
	report, err := Run(context.Background(), configuration)
	if err != nil || report.Expected != 7 || report.Totals.Planned != 7 {
		t.Fatalf("浮点误差在窗口右边界多产生请求: err=%v expected=%d planned=%d", err, report.Expected, report.Totals.Planned)
	}
}

func TestClosedRunnerErrorsAndRedaction(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch requests.Add(1) % 3 {
		case 0:
			writer.WriteHeader(503)
		case 1:
			writer.Header().Set("Content-Type", "application/json")
			fmt.Fprint(writer, `{"secret":"test-only-secret-do-not-log"}`)
		case 2:
			respondCatalog(writer, request)
		}
	}))
	defer server.Close()
	configuration := testConfig()
	configuration.URL, configuration.Scenario, configuration.TenantCount = server.URL, "catalog", 1
	configuration.ErrorSampleLimit = 2
	report, err := Run(context.Background(), configuration)
	if err != nil {
		t.Fatal(err)
	}
	assertConservation(t, report)
	if report.Totals.Success == 0 || report.Totals.StatusErrors == 0 || report.Totals.SemanticErrors == 0 || len(report.Errors) != 2 {
		t.Fatalf("错误分类错误: %#v", report.Totals)
	}
	encoded, err := json.Marshal(report)
	if err != nil || strings.Contains(string(encoded), configuration.Secret) || strings.Contains(string(encoded), "Bearer ") {
		t.Fatalf("报告泄露凭据或无法编码: %v", err)
	}
}

func TestCancellationDrainsRequests(t *testing.T) {
	var inflight atomic.Int64
	entered := make(chan struct{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		inflight.Add(1)
		defer inflight.Add(-1)
		entered <- struct{}{}
		<-request.Context().Done()
	}))
	defer server.Close()
	configuration := testConfig()
	configuration.URL, configuration.Scenario, configuration.Duration = server.URL, "catalog", time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-entered; cancel() }()
	start := time.Now()
	report, err := Run(ctx, configuration)
	if err == nil || !report.Canceled || time.Since(start) > time.Second {
		t.Fatalf("取消没有及时返回: %v %#v", err, report.Totals)
	}
	assertConservation(t, report)
	if report.Totals.TransportErrors == 0 {
		t.Fatal("取消的在途请求被隐藏")
	}
}

func TestRunnerRejectsRedirectsAndInvalidConfiguration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, "http://example.com/", http.StatusFound)
	}))
	defer server.Close()
	configuration := testConfig()
	configuration.URL = server.URL
	report, err := Run(context.Background(), configuration)
	if err != nil || report.Totals.StatusErrors == 0 || report.Totals.Success != 0 {
		t.Fatalf("重定向未被隔离: %v %#v", err, report.Totals)
	}
	configuration.Rate = -1
	if _, err := Run(context.Background(), configuration); err == nil {
		t.Fatal("无效运行配置被接受")
	}
}

func TestRunnerUsesCompleteBusinessRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Proto != "HTTP/1.1" || !strings.HasPrefix(request.Header.Get("Authorization"), "Bearer 1.") {
			t.Error("请求未采用统一HTTP1.1或认证协议")
		}
		writer.Header().Set("Content-Type", "application/json")
		switch {
		case request.URL.Path == "/api/catalog":
			if request.URL.Query().Get("limit") != "20" || request.URL.Query().Get("category_id") == "" {
				t.Error("目录筛选参数缺失")
			}
			respondCatalog(writer, request)
		case strings.HasPrefix(request.URL.Path, "/api/products/"):
			id, _ := strconv.ParseInt(strings.TrimPrefix(request.URL.Path, "/api/products/"), 10, 64)
			if id <= 111 {
				writer.Header().Set("X-Benchmark-Cache", "hit")
			} else {
				writer.Header().Set("X-Benchmark-Cache", "miss")
			}
			fmt.Fprintf(writer, `{"id":%d,"name":"商品","price_cents":100,"stock":10,"category":"分类","description":"真实商品说明","specs":[{"name":"尺寸","value":"标准"}]}`, id)
		case request.URL.Path == "/api/orders":
			var input struct {
				Items []orderItem `json:"items"`
			}
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil || len(input.Items) != 2 || request.Method != http.MethodPost || len(request.Header.Get("Idempotency-Key")) != 32 {
				t.Errorf("订单输入协议错误: %v", err)
				writer.WriteHeader(400)
				return
			}
			writer.WriteHeader(http.StatusCreated)
			fmt.Fprintf(writer, `{"order_id":1,"total_cents":%d,"status":"created","items":[{"product_id":%d,"quantity":%d,"unit_price_cents":100},{"product_id":%d,"quantity":%d,"unit_price_cents":100}]}`,
				(input.Items[0].Quantity+input.Items[1].Quantity)*100, input.Items[0].ProductID, input.Items[0].Quantity, input.Items[1].ProductID, input.Items[1].Quantity)
		case strings.HasPrefix(request.URL.Path, "/api/tickets/"):
			path := strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, "/api/tickets/"), "/comments")
			id, _ := strconv.ParseInt(path, 10, 64)
			var input struct {
				Body string `json:"body"`
			}
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil || len([]rune(input.Body)) < 32 {
				t.Errorf("工单正文不满足合同: %v", err)
			}
			writer.WriteHeader(http.StatusCreated)
			fmt.Fprintf(writer, `{"comment_id":1,"ticket_id":%d,"version":1,"status":"open"}`, id)
		default:
			writer.WriteHeader(404)
		}
	}))
	defer server.Close()
	configuration := testConfig()
	configuration.URL, configuration.Scenario, configuration.TenantCount = server.URL, "mixed", 1
	configuration.Duration = 100 * time.Millisecond
	report, err := Run(context.Background(), configuration)
	if err != nil {
		t.Fatal(err)
	}
	assertConservation(t, report)
	if report.Totals.SemanticErrors != 0 || report.Totals.StatusErrors != 0 || report.Totals.TransportErrors != 0 || report.Totals.CacheHits == 0 || report.Totals.CacheMisses == 0 {
		t.Fatalf("完整业务请求未通过: %#v samples=%#v", report.Totals, report.Errors)
	}
	for _, scenario := range []string{"catalog", "detail", "order", "ticket"} {
		if report.Scenarios[scenario].Totals.Success == 0 {
			t.Fatalf("场景未实际执行: %s", scenario)
		}
	}
}

func TestTimeoutAndResponseSizeAreVisible(t *testing.T) {
	for _, mode := range []string{"timeout", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if mode == "timeout" {
					<-request.Context().Done()
					return
				}
				writer.Header().Set("Content-Type", "application/json")
				fmt.Fprint(writer, strings.Repeat("x", maximumResponseBytes+1))
			}))
			defer server.Close()
			configuration := testConfig()
			configuration.URL, configuration.Scenario, configuration.Concurrency = server.URL, "catalog", 1
			if mode == "timeout" {
				configuration.Timeout = 5 * time.Millisecond
			}
			report, err := Run(context.Background(), configuration)
			if err != nil {
				t.Fatal(err)
			}
			assertConservation(t, report)
			if report.Totals.Success != 0 || len(report.Errors) == 0 {
				t.Fatalf("错误请求被计为成功: %#v", report.Totals)
			}
			if mode == "timeout" && report.Errors[0].Reason != "request_timeout" {
				t.Fatalf("超时错误分类丢失: %#v", report.Errors)
			}
			if mode == "oversize" && report.Totals.SemanticErrors == 0 {
				t.Fatalf("超限正文错误分类丢失: %#v", report.Errors)
			}
		})
	}
}
