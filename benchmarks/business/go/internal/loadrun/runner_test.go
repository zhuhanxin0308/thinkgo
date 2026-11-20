package loadrun

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fixtureCatalogPage 独立枚举 SQL 合同并全量排序，校验生产门禁的有界分页算法。
func fixtureCatalogPage(c Config, category int) string {
	type row struct{ id, price int64 }
	rows := []row{}
	for local := category; local <= c.ProductsPerTenant; local += c.Categories {
		price := 100 + (int64(local)*7919+c.DatasetSeed)%199900
		if local%c.InactiveProductModulus != 0 && price <= c.MaximumPriceCents {
			rows = append(rows, row{int64(local), price})
		}
	}
	slices.SortStableFunc(rows, func(a, b row) int { return int(a.price - b.price) })
	cursor := "null"
	if len(rows) > c.CatalogPageSize {
		rows = rows[:c.CatalogPageSize]
		last := rows[len(rows)-1]
		cursor = fmt.Sprintf(`{"id":%d,"price_cents":%d}`, last.id, last.price)
	}
	cards := make([]string, len(rows))
	for index, row := range rows {
		cards[index] = fmt.Sprintf(`{"id":%d,"name":"通用商品-1-%d","price_cents":%d,"stock":10,"category":"分类-%d"}`, row.id, row.id, row.price, category)
	}
	return fmt.Sprintf(`{"items":[%s],"next_cursor":%s}`, strings.Join(cards, ","), cursor)
}

var fixtureCatalog = func() map[int]string {
	pages := make(map[int]string)
	for category := 1; category <= defaultCategories; category++ {
		pages[category] = fixtureCatalogPage(DefaultConfig(), category)
	}
	return pages
}()

// respondCatalog 仅作为本地测试端点，不替代正式业务应用。
func respondCatalog(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Content-Type", "application/json")
	category, err := strconv.Atoi(request.URL.Query().Get("category_id"))
	if err != nil {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	fmt.Fprint(writer, fixtureCatalog[category])
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

	// 验证 float64(sequence)/Rate*1e9 发生向上舍入触碰 Duration 时，有理数向下取整偏移仍严格落在窗口内且零丢包。
	boundaryCases := []struct {
		duration time.Duration
		rate     float64
		expected uint64
	}{
		{duration: 7 * time.Second, rate: 0.4285714285714286, expected: 4},
		{duration: 70 * time.Millisecond, rate: 42.85714285714286, expected: 4},
	}
	for _, tc := range boundaryCases {
		cfg := configuration
		cfg.Duration, cfg.Rate = tc.duration, tc.rate
		if got := plannedRequestCount(cfg); got != tc.expected {
			t.Fatalf("plannedRequestCount(%v, %v) = %d, want %d", tc.duration, tc.rate, got, tc.expected)
		}
		rateRat := parseRateRational(tc.rate)
		lastOffset := scheduledOffset(tc.expected-1, rateRat)
		if lastOffset >= tc.duration {
			t.Fatalf("末个计划请求偏移越界: offset=%v duration=%v", lastOffset, tc.duration)
		}
	}

	configuration.Duration, configuration.Rate = 80*time.Millisecond, 37.5
	configuration.MaxInflight = 16
	roundedReport, err := Run(context.Background(), configuration)
	if err != nil || roundedReport.Expected != 3 || roundedReport.Totals.Dropped != 0 || roundedReport.Totals.Success != 3 {
		t.Fatalf("非整到达率运行丢包: err=%v expected=%d totals=%#v errors=%#v", err, roundedReport.Expected, roundedReport.Totals, roundedReport.Errors)
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
			fmt.Fprintf(writer, `{"id":%d,"name":"通用商品-1-%d","price_cents":%d,"stock":10,"category":"分类-%d","description":"真实商品说明","specs":[{"name":"尺寸","value":"标准"}]}`, id, id, 100+(id*7919+defaultDatasetSeed)%199900, (id-1)%defaultCategories+1)
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
			first, second := input.Items[0], input.Items[1]
			firstPrice := 100 + (first.ProductID*7919+defaultDatasetSeed)%199900
			secondPrice := 100 + (second.ProductID*7919+defaultDatasetSeed)%199900
			fmt.Fprintf(writer, `{"order_id":1,"total_cents":%d,"status":"created","items":[{"product_id":%d,"quantity":%d,"unit_price_cents":%d},{"product_id":%d,"quantity":%d,"unit_price_cents":%d}]}`,
				first.Quantity*firstPrice+second.Quantity*secondPrice, first.ProductID, first.Quantity, firstPrice, second.ProductID, second.Quantity, secondPrice)
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
			fmt.Fprintf(writer, `{"comment_id":15001,"ticket_id":%d,"version":4,"status":"open"}`, id)
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
