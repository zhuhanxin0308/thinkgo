package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/signal"
	"regexp"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	hdrhistogram "github.com/HdrHistogram/hdrhistogram-go"
	"thinkgo/framework"
	fwcontext "thinkgo/framework/context"
	"thinkgo/framework/db"
	"thinkgo/framework/env"
	fwhttp "thinkgo/framework/http"
	"thinkgo/framework/log"
	"thinkgo/framework/route"
)

const (
	productCount  = 100000
	customerCount = 50000
	orderCount    = 100000

	localBenchmarkTLSMode    = "skip-verify"
	verifiedBenchmarkTLSMode = "true"

	// lowestDiscernibleValue 是延迟直方图的精度边界，低于它的非负值仍然允许记录。
	lowestDiscernibleValue int64 = int64(time.Microsecond)
	// highestTrackableValue 是单个延迟样本的硬上限，覆盖 HTTP 客户端超时及异常抖动。
	highestTrackableValue  int64 = int64(time.Hour)
	significantValueDigits       = 3
)

var checkoutSequence atomic.Int64
var benchmarkTablePrefix = "bench_"

var benchmarkTablePrefixPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,31}_$`)

func main() {
	mode := flag.String("mode", "serve", "prepare, serve, full-serve, native, or load")
	addr := flag.String("addr", "127.0.0.1:18081", "HTTP listen address")
	baseURL := flag.String("base-url", "http://127.0.0.1:18081", "benchmark service URL")
	duration := flag.Duration("duration", 30*time.Second, "load-test duration")
	concurrency := flag.Int("concurrency", 32, "number of concurrent HTTP workers")
	loadScenario := flag.String("scenario", "mixed", "mixed, health, product, catalog, order, checkout, or full feature scenarios")
	logBuffer := flag.Int("log-buffer", 0, "access-log async buffer for control experiments; 0 keeps framework default")
	accessLog := flag.Bool("access-log", true, "write per-request access logs")
	cpuProfile := flag.String("cpu-profile", "", "optional CPU profile output path for the framework server")
	heapProfile := flag.String("heap-profile", "", "optional heap profile output path for full-serve")
	mutexProfile := flag.String("mutex-profile", "", "optional mutex profile output path for full-serve")
	blockProfile := flag.String("block-profile", "", "optional blocking profile output path for full-serve")
	cpuProfileDuration := flag.Duration("cpu-profile-duration", 15*time.Second, "CPU profile duration")
	tablePrefix := flag.String("table-prefix", os.Getenv("THINKGO_BENCHMARK_TABLE_PREFIX"), "isolated benchmark table prefix, must end with _")
	flag.Parse()
	if *tablePrefix != "" {
		if err := setBenchmarkTablePrefix(*tablePrefix); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}

	if *mode == "load" {
		if err := runLoad(*baseURL, *duration, *concurrency, *loadScenario); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if *mode == "native" {
		if err := serveNative(*addr); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if *mode == "full-serve" {
		runtime, err := newFullBenchmarkRuntime()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer func() { _ = runtime.Close() }()
		if err := serveFull(runtime, *addr, *accessLog, *cpuProfile, *heapProfile, *mutexProfile, *blockProfile, *cpuProfileDuration); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	app, err := newBenchmarkApp()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer func() { _ = app.Close() }()

	switch *mode {
	case "prepare":
		if err := prepare(app); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "serve":
		if err := serve(app, *addr, *logBuffer, *accessLog, *cpuProfile, *cpuProfileDuration); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "mode must be prepare, serve, full-serve, native, or load")
		os.Exit(2)
	}
}

func serveNative(addr string) error {
	server := &http.Server{
		Addr:              addr,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"ok":true}`))
		}),
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() { <-stop; _ = server.Shutdown(context.Background()) }()
	fmt.Printf("native benchmark server listening on http://%s\n", addr)
	err := server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// newBenchmarkApp 复用框架的 HTTP、路由和 ORM 实现，并只为本机压测连接关闭
// MySQL 证书校验；生产连接应配置受信任 CA，而不是使用 skip-verify。
func newBenchmarkApp() (*framework.App, error) {
	app, err := framework.BuildConsoleApp()
	if err != nil {
		return nil, fmt.Errorf("benchmark application initialization failed: %w", err)
	}
	database, err := connectBenchmarkDatabase(app)
	if err != nil {
		_ = app.Close()
		return nil, fmt.Errorf("benchmark database connection failed: %w", err)
	}
	manager, err := framework.ResolveServiceAs[*db.Manager](app, framework.ServiceDBManager)
	if err != nil {
		manager = db.NewManager("mysql")
		app.Instance(string(framework.ServiceDBManager), manager)
	}
	if err := manager.Add("mysql", database); err != nil {
		_ = database.Close()
		_ = app.Close()
		return nil, err
	}
	app.Instance(string(framework.ServiceDB), database)
	return app, nil
}

func envOr(app *framework.App, key, fallback string) string {
	if environment, err := framework.ResolveServiceAs[*env.Env](app, framework.ServiceEnv); err == nil && environment != nil {
		if value := environment.Get(key); value != "" {
			return value
		}
	}
	return fallback
}

func envInt(app *framework.App, key string, fallback int) int {
	value, err := strconv.Atoi(envOr(app, key, ""))
	if err != nil || value < 0 {
		return fallback
	}
	return value
}

// envBool 读取可选布尔配置，非法值回退到兼容默认值。
func envBool(app *framework.App, key string, fallback bool) bool {
	return parseBoolOrFallback(envOr(app, key, ""), fallback)
}

// parseBoolOrFallback 解析布尔文本，空值和非法值都保留调用方默认值。
func parseBoolOrFallback(value string, fallback bool) bool {
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

// benchmarkDBParams 为本机回环基准保留自签名证书兼容性，远程数据库默认启用证书校验。
// 调用方可通过 DB_TLS_MODE 显式选择 MySQL 驱动支持的 TLS 配置名称。
func benchmarkDBParams(app *framework.App) map[string]string {
	tlsMode := strings.TrimSpace(envOr(app, "DB_TLS_MODE", ""))
	if tlsMode == "" {
		if isLoopbackBenchmarkHost(envOr(app, "DB_HOST", "127.0.0.1")) {
			tlsMode = localBenchmarkTLSMode
		} else {
			tlsMode = verifiedBenchmarkTLSMode
		}
	}
	params := map[string]string{"tls": tlsMode}
	if envBool(app, "DB_INTERPOLATE_PARAMS", false) {
		params["interpolateParams"] = "true"
	}
	return params
}

// isLoopbackBenchmarkHost 仅把明确的本机地址视为可使用本地自签名证书的基准目标。
func isLoopbackBenchmarkHost(host string) bool {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func prepare(app *framework.App) error {
	if strings.TrimSpace(os.Getenv("THINKGO_BENCHMARK_CONFIRM")) != "prepare" {
		return errors.New("benchmark prepare 会删除并重建隔离表；请设置 THINKGO_BENCHMARK_CONFIRM=prepare 明确确认")
	}
	if benchmarkTablePrefix == "bench_" {
		return errors.New("benchmark prepare 必须使用非默认的 -table-prefix 隔离表前缀")
	}
	database, err := framework.ResolveServiceAs[*db.DB](app, framework.ServiceDB)
	if err != nil || database == nil {
		return errors.New("benchmark database is unavailable")
	}
	customers, products, orders, items := benchmarkTable("customers"), benchmarkTable("products"), benchmarkTable("orders"), benchmarkTable("order_items")
	statements := []string{
		"DROP TABLE IF EXISTS " + items,
		"DROP TABLE IF EXISTS " + orders,
		"DROP TABLE IF EXISTS " + products,
		"DROP TABLE IF EXISTS " + customers,
		fmt.Sprintf("CREATE TABLE %s (id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, email VARCHAR(120) NOT NULL, name VARCHAR(100) NOT NULL, tier TINYINT UNSIGNED NOT NULL, created_at DATETIME NOT NULL, PRIMARY KEY (id), UNIQUE KEY uq_email (email), KEY idx_tier_id (tier, id)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4", customers),
		fmt.Sprintf("CREATE TABLE %s (id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, sku VARCHAR(40) NOT NULL, name VARCHAR(160) NOT NULL, category_id INT UNSIGNED NOT NULL, price DECIMAL(10,2) NOT NULL, stock INT UNSIGNED NOT NULL, created_at DATETIME NOT NULL, update_time BIGINT NOT NULL DEFAULT 0, create_time BIGINT NOT NULL DEFAULT 0, PRIMARY KEY (id), UNIQUE KEY uq_sku (sku), KEY idx_category_id (category_id, id)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4", products),
		fmt.Sprintf("CREATE TABLE %s (id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, order_no VARCHAR(48) NOT NULL, customer_id BIGINT UNSIGNED NOT NULL, status TINYINT UNSIGNED NOT NULL, total_amount DECIMAL(12,2) NOT NULL, created_at DATETIME NOT NULL, update_time BIGINT NOT NULL DEFAULT 0, create_time BIGINT NOT NULL DEFAULT 0, PRIMARY KEY (id), UNIQUE KEY uq_order_no (order_no), KEY idx_customer_status_created (customer_id, status, created_at), KEY idx_status_created (status, created_at)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4", orders),
		fmt.Sprintf("CREATE TABLE %s (id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, order_id BIGINT UNSIGNED NOT NULL, product_id BIGINT UNSIGNED NOT NULL, quantity INT UNSIGNED NOT NULL, unit_price DECIMAL(10,2) NOT NULL, create_time BIGINT NOT NULL DEFAULT 0, update_time BIGINT NOT NULL DEFAULT 0, PRIMARY KEY (id), KEY idx_order_id (order_id), KEY idx_product_id (product_id)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4", items),
		sequenceInsert(customers, "id, email, name, tier, created_at", "n + 1, CONCAT('customer', n + 1, '@bench.local'), CONCAT('Customer ', n + 1), MOD(n, 20), NOW()", customerCount),
		sequenceInsert(products, "id, sku, name, category_id, price, stock, created_at", "n + 1, CONCAT('SKU-', LPAD(n + 1, 8, '0')), CONCAT('Product ', n + 1), MOD(n, 200) + 1, ROUND(5 + MOD(n, 5000) * 0.17, 2), 1000, NOW()", productCount),
		sequenceInsert(orders, "id, order_no, customer_id, status, total_amount, created_at", "n + 1, CONCAT('seed-', LPAD(n + 1, 10, '0')), MOD(n, 50000) + 1, MOD(n, 5), 0, TIMESTAMPADD(SECOND, -MOD(n, 2592000), NOW())", orderCount),
		fmt.Sprintf("INSERT INTO %s (order_id, product_id, quantity, unit_price) SELECT o.id, MOD(o.id * 13 + d.n * 97, 100000) + 1, MOD(o.id + d.n, 5) + 1, ROUND(5 + MOD(o.id * 13 + d.n * 97, 5000) * 0.17, 2) FROM %s o JOIN (SELECT 0 AS n UNION ALL SELECT 1 UNION ALL SELECT 2) d", items, orders),
		fmt.Sprintf("UPDATE %s o JOIN (SELECT order_id, SUM(quantity * unit_price) AS total FROM %s GROUP BY order_id) i ON i.order_id = o.id SET o.total_amount = i.total", orders, items),
	}
	for _, statement := range statements {
		if _, err := database.Execute(statement); err != nil {
			return fmt.Errorf("prepare benchmark data: %w", err)
		}
	}
	rows, err := database.Query(fmt.Sprintf("SELECT (SELECT COUNT(*) FROM %s) AS customers, (SELECT COUNT(*) FROM %s) AS products, (SELECT COUNT(*) FROM %s) AS orders, (SELECT COUNT(*) FROM %s) AS items", customers, products, orders, items))
	if err != nil {
		return err
	}
	return printJSON(map[string]interface{}{"prepared": true, "rows": rows[0]})
}

func sequenceInsert(table, fields, values string, count int) string {
	return fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM (SELECT a.n + 10 * b.n + 100 * c.n + 1000 * d.n + 10000 * e.n AS n FROM (SELECT 0 AS n UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4 UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9) a CROSS JOIN (SELECT 0 AS n UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4 UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9) b CROSS JOIN (SELECT 0 AS n UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4 UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9) c CROSS JOIN (SELECT 0 AS n UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4 UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9) d CROSS JOIN (SELECT 0 AS n UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4 UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9) e) sequence_data WHERE n < %d", table, fields, values, count)
}

func serve(app *framework.App, addr string, logBuffer int, accessLog bool, cpuProfilePath string, cpuProfileDuration time.Duration) error {
	if _, err := framework.ResolveServiceAs[*db.DB](app, framework.ServiceDB); err != nil {
		return errors.New("benchmark database is unavailable")
	}
	logger, loggerErr := framework.ResolveServiceAs[*log.Log](app, framework.ServiceLog)
	if loggerErr != nil {
		return fmt.Errorf("benchmark log is unavailable: %w", loggerErr)
	}
	if logBuffer > 0 && logger != nil {
		if err := logger.SetBufferSize(logBuffer); err != nil {
			return fmt.Errorf("set benchmark log buffer: %w", err)
		}
		if err := logger.SetFlushInterval(time.Second); err != nil {
			return fmt.Errorf("set benchmark log flush interval: %w", err)
		}
	}
	if !accessLog && logger != nil {
		logger.SetLevels([]string{"warning", "error"})
	}
	if cpuProfilePath != "" {
		if cpuProfileDuration <= 0 {
			return errors.New("CPU profile duration must be positive")
		}
		// #nosec G304 -- cpuProfilePath 是显式的本地压测输出路径，不来自 HTTP 请求。
		file, err := os.Create(cpuProfilePath)
		if err != nil {
			return fmt.Errorf("create CPU profile: %w", err)
		}
		if err = pprof.StartCPUProfile(file); err != nil {
			_ = file.Close()
			return fmt.Errorf("start CPU profile: %w", err)
		}
		go func() {
			time.Sleep(cpuProfileDuration)
			pprof.StopCPUProfile()
			_ = file.Close()
		}()
	}
	if err := registerRoutes(app); err != nil {
		return err
	}
	kernel, err := fwhttp.NewHttp(app)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: addr, Handler: kernel, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() { <-stop; _ = server.Shutdown(context.Background()) }()
	fmt.Printf("benchmark server listening on http://%s\n", addr)
	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func registerRoutes(app *framework.App) error {
	router, err := framework.ResolveServiceAs[*route.Router](app, framework.ServiceRoute)
	if err != nil {
		return err
	}
	database, err := framework.ResolveServiceAs[*db.DB](app, framework.ServiceDB)
	if err != nil {
		return err
	}
	if _, err := router.Get("/bench/health", func(req *fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Json(map[string]interface{}{"ok": true})
	}); err != nil {
		return err
	}
	if _, err := router.Get("/bench/products/:id", func(req *fwcontext.Request) *fwcontext.Response {
		id := req.ParamInt64("id", 0)
		row, err := database.Table(benchmarkTable("products")).WithContext(req.Raw().Context()).WhereField("id", "=", id).Field("id,sku,name,category_id,price,stock").Find()
		if err != nil {
			return dbError(err)
		}
		if row == nil {
			return fwcontext.NewResponse().Code(http.StatusNotFound).Json(map[string]string{"error": "product not found"})
		}
		return fwcontext.NewResponse().Json(row)
	}); err != nil {
		return err
	}
	if _, err := router.Get("/bench/catalog", func(req *fwcontext.Request) *fwcontext.Response {
		categoryID := boundedInt(req.Get("category_id", "1"), 1, 200, 1)
		page := boundedInt(req.Get("page", "1"), 1, 1000, 1)
		rows, err := database.Table(benchmarkTable("products")).WithContext(req.Raw().Context()).WhereField("category_id", "=", categoryID).Field("id,sku,name,price,stock").Order("id DESC").Page(page, 20).Select()
		if err != nil {
			return dbError(err)
		}
		return fwcontext.NewResponse().Json(map[string]interface{}{"category_id": categoryID, "page": page, "products": rows})
	}); err != nil {
		return err
	}
	if _, err := router.Get("/bench/orders/:id", func(req *fwcontext.Request) *fwcontext.Response {
		id := req.ParamInt64("id", 0)
		rows, err := database.QueryContext(req.Raw().Context(), fmt.Sprintf("SELECT o.id, o.order_no, o.status, o.total_amount, o.created_at, c.id AS customer_id, c.name AS customer_name, COUNT(oi.id) AS item_count, SUM(oi.quantity) AS item_quantity FROM %s o JOIN %s c ON c.id = o.customer_id JOIN %s oi ON oi.order_id = o.id WHERE o.id = ? GROUP BY o.id, o.order_no, o.status, o.total_amount, o.created_at, c.id, c.name", benchmarkTable("orders"), benchmarkTable("customers"), benchmarkTable("order_items")), id)
		if err != nil {
			return dbError(err)
		}
		if len(rows) == 0 {
			return fwcontext.NewResponse().Code(http.StatusNotFound).Json(map[string]string{"error": "order not found"})
		}
		return fwcontext.NewResponse().Json(rows[0])
	}); err != nil {
		return err
	}
	if _, err := router.Post("/bench/checkout", func(req *fwcontext.Request) *fwcontext.Response {
		sequence := checkoutSequence.Add(1)
		customerID := sequence%customerCount + 1
		productID := sequence%productCount + 1
		var orderID int64
		err := database.TransactionContext(req.Raw().Context(), func(tx *db.Tx) error {
			product, err := tx.Table(benchmarkTable("products")).WhereField("id", "=", productID).Lock().Find()
			if err != nil {
				return err
			}
			if product == nil {
				return errors.New("benchmark product not found")
			}
			price, ok := product["price"]
			if !ok {
				return errors.New("benchmark product price not found")
			}
			if _, err = tx.Table(benchmarkTable("products")).WhereField("id", "=", productID).WhereField("stock", ">=", 1).Dec("stock", 1).Update(nil); err != nil {
				return err
			}
			orderID, err = tx.Table(benchmarkTable("orders")).Insert(map[string]interface{}{"order_no": fmt.Sprintf("load-%d", time.Now().UnixNano()+sequence), "customer_id": customerID, "status": 1, "total_amount": price, "created_at": time.Now()})
			if err != nil {
				return err
			}
			_, err = tx.Table(benchmarkTable("order_items")).Insert(map[string]interface{}{"order_id": orderID, "product_id": productID, "quantity": 1, "unit_price": price})
			return err
		})
		if err != nil {
			return dbError(err)
		}
		return fwcontext.NewResponse().Code(http.StatusCreated).Json(map[string]interface{}{"order_id": orderID})
	}); err != nil {
		return err
	}
	return nil
}

func dbError(err error) *fwcontext.Response {
	return fwcontext.NewResponse().Code(http.StatusInternalServerError).Json(map[string]string{"error": err.Error()})
}

func boundedInt(raw string, minimum, maximum, fallback int) int {
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum || value > maximum {
		return fallback
	}
	return value
}

func setBenchmarkTablePrefix(prefix string) error {
	prefix = strings.TrimSpace(prefix)
	if !benchmarkTablePrefixPattern.MatchString(prefix) {
		return fmt.Errorf("benchmark table prefix 非法，必须匹配 %s", benchmarkTablePrefixPattern.String())
	}
	benchmarkTablePrefix = prefix
	return nil
}

func benchmarkTable(name string) string {
	return benchmarkTablePrefix + name
}

type workerStats struct {
	total, failures int64
	histogram       *hdrhistogram.Histogram
	recordErr       error
}
type result struct {
	Scenario        string  `json:"scenario"`
	DurationSeconds float64 `json:"duration_seconds"`
	Concurrency     int     `json:"concurrency"`
	Requests        int64   `json:"requests"`
	Failures        int64   `json:"failures"`
	RPS             float64 `json:"requests_per_second"`
	P50MS           float64 `json:"p50_ms"`
	P95MS           float64 `json:"p95_ms"`
	P99MS           float64 `json:"p99_ms"`
	MaxMS           float64 `json:"max_ms"`
}

func runLoad(baseURL string, duration time.Duration, concurrency int, loadScenario string) error {
	if duration <= 0 || concurrency <= 0 {
		return errors.New("duration and concurrency must be positive")
	}
	if loadScenario != "mixed" && loadScenario != "health" && loadScenario != "product" && loadScenario != "catalog" && loadScenario != "order" && loadScenario != "checkout" && loadScenario != "full" && loadScenario != "full-business" && loadScenario != "full-cache" && loadScenario != "full-cache-miss" && loadScenario != "full-invalid" {
		return errors.New("scenario must be mixed, health, product, catalog, order, checkout, full, full-business, full-cache, full-cache-miss, or full-invalid")
	}
	transport := &http.Transport{MaxIdleConns: concurrency * 2, MaxIdleConnsPerHost: concurrency * 2, MaxConnsPerHost: concurrency * 2, IdleConnTimeout: 30 * time.Second}
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second}
	defer transport.CloseIdleConnections()
	deadline := time.Now().Add(duration)
	stats := make(chan workerStats, concurrency)
	var workers sync.WaitGroup
	for worker := 0; worker < concurrency; worker++ {
		workers.Add(1)
		go func(seed uint64) {
			defer workers.Done()
			// #nosec G404 -- 压测场景只选择请求样本，不生成安全凭据。
			rng := rand.New(rand.NewPCG(seed, seed+1))
			local := workerStats{histogram: newLatencyHistogram()}
			workerClient := *client
			if strings.HasPrefix(loadScenario, "full") {
				var err error
				workerClient.Jar, err = cookiejar.New(nil)
				if err != nil {
					local.failures++
					stats <- local
					return
				}
			}
			for time.Now().Before(deadline) {
				method, url := scenario(baseURL, rng, loadScenario)
				request, err := newLoadRequest(method, url)
				if err != nil {
					local.failures++
					continue
				}
				started := time.Now()
				response, err := workerClient.Do(request)
				elapsed := time.Since(started)
				local.total++
				if local.recordErr = recordLatency(local.histogram, elapsed); local.recordErr != nil {
					break
				}
				if err != nil {
					local.failures++
					continue
				}
				_, _ = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
				if response.StatusCode < 200 || response.StatusCode >= 300 {
					local.failures++
				}
			}
			stats <- local
		}(uint64(worker + 1))
	}
	workers.Wait()
	close(stats)
	summary := result{Scenario: loadScenario, DurationSeconds: duration.Seconds(), Concurrency: concurrency}
	mergedHistogram := newLatencyHistogram()
	for local := range stats {
		if local.recordErr != nil {
			return fmt.Errorf("record latency: %w", local.recordErr)
		}
		summary.Requests += local.total
		summary.Failures += local.failures
		if err := mergeLatencyHistogram(mergedHistogram, local.histogram); err != nil {
			return err
		}
	}
	if mergedHistogram.TotalCount() == 0 {
		return errors.New("load test completed without requests")
	}
	summary.RPS = float64(summary.Requests) / duration.Seconds()
	summary.P50MS = histogramLatencyAt(mergedHistogram, 0.50)
	summary.P95MS = histogramLatencyAt(mergedHistogram, 0.95)
	summary.P99MS = histogramLatencyAt(mergedHistogram, 0.99)
	summary.MaxMS = histogramMaxMilliseconds(mergedHistogram)
	return printJSON(summary)
}

func newLoadRequest(method, url string) (*http.Request, error) {
	var body io.Reader
	if strings.Contains(url, "/bench/full/business") && method == http.MethodPost {
		body = strings.NewReader(`{"email":"bench@example.com","name":"Ada","quantity":1}`)
	}
	request, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept-Language", "en-us,en;q=0.8")
	return request, nil
}

func scenario(baseURL string, rng *rand.Rand, loadScenario string) (string, string) {
	if loadScenario == "health" {
		return http.MethodGet, baseURL + "/bench/health"
	}
	if loadScenario == "product" {
		return http.MethodGet, fmt.Sprintf("%s/bench/products/%d", baseURL, rng.IntN(productCount)+1)
	}
	if loadScenario == "catalog" {
		return http.MethodGet, fmt.Sprintf("%s/bench/catalog?category_id=%d&page=%d", baseURL, rng.IntN(200)+1, rng.IntN(10)+1)
	}
	if loadScenario == "order" {
		return http.MethodGet, fmt.Sprintf("%s/bench/orders/%d", baseURL, rng.IntN(orderCount)+1)
	}
	if loadScenario == "checkout" {
		return http.MethodPost, baseURL + "/bench/checkout"
	}
	if loadScenario == "full" {
		switch roll := rng.IntN(100); {
		case roll < 65:
			return http.MethodPost, fmt.Sprintf("%s/bench/full/business?product_id=%d", baseURL, rng.IntN(productCount)+1)
		case roll < 85:
			return http.MethodGet, fmt.Sprintf("%s/bench/full/cache?cache_key=bench-full-cache-%d", baseURL, rng.IntN(32))
		case roll < 95:
			return http.MethodGet, fmt.Sprintf("%s/bench/full/cache?cache_key=bench-full-miss-%d", baseURL, rng.IntN(1_000_000_000))
		default:
			return http.MethodPost, baseURL + "/bench/full/business?invalid=1"
		}
	}
	if loadScenario == "full-business" {
		return http.MethodPost, baseURL + "/bench/full/business?product_id=7"
	}
	if loadScenario == "full-cache" {
		return http.MethodGet, fmt.Sprintf("%s/bench/full/cache?cache_key=bench-full-cache-%d", baseURL, rng.IntN(32))
	}
	if loadScenario == "full-cache-miss" {
		return http.MethodGet, fmt.Sprintf("%s/bench/full/cache?cache_key=bench-full-miss-%d", baseURL, rng.IntN(1_000_000_000))
	}
	if loadScenario == "full-invalid" {
		return http.MethodPost, baseURL + "/bench/full/business?invalid=1"
	}
	switch roll := rng.IntN(100); {
	case roll < 45:
		return http.MethodGet, fmt.Sprintf("%s/bench/products/%d", baseURL, rng.IntN(productCount)+1)
	case roll < 75:
		return http.MethodGet, fmt.Sprintf("%s/bench/catalog?category_id=%d&page=%d", baseURL, rng.IntN(200)+1, rng.IntN(10)+1)
	case roll < 95:
		return http.MethodGet, fmt.Sprintf("%s/bench/orders/%d", baseURL, rng.IntN(orderCount)+1)
	default:
		return http.MethodPost, baseURL + "/bench/checkout"
	}
}

var errLatencyHistogramUnavailable = errors.New("latency histogram unavailable")

func newLatencyHistogram() *hdrhistogram.Histogram {
	return hdrhistogram.New(lowestDiscernibleValue, highestTrackableValue, significantValueDigits)
}

func recordLatency(histogram *hdrhistogram.Histogram, elapsed time.Duration) error {
	if histogram == nil {
		return errLatencyHistogramUnavailable
	}
	if elapsed < 0 {
		return fmt.Errorf("latency cannot be negative: %s", elapsed)
	}
	value := elapsed.Nanoseconds()
	if value > highestTrackableValue {
		return fmt.Errorf("latency %s is outside histogram bounds", elapsed)
	}
	if err := histogram.RecordValue(value); err != nil {
		return fmt.Errorf("latency %s is outside histogram bounds: %w", elapsed, err)
	}
	return nil
}

func mergeLatencyHistogram(destination, source *hdrhistogram.Histogram) error {
	if destination == nil || source == nil {
		return errLatencyHistogramUnavailable
	}
	if dropped := destination.Merge(source); dropped != 0 {
		return fmt.Errorf("latency histogram merge dropped %d samples", dropped)
	}
	return nil
}

func histogramLatencyAt(histogram *hdrhistogram.Histogram, percentile float64) float64 {
	if histogram == nil || histogram.TotalCount() == 0 {
		return 0
	}
	if percentile < 0 {
		percentile = 0
	} else if percentile > 1 {
		percentile = 1
	}
	return float64(histogram.ValueAtQuantile(percentile*100)) / float64(time.Millisecond)
}

func histogramMaxMilliseconds(histogram *hdrhistogram.Histogram) float64 {
	if histogram == nil || histogram.TotalCount() == 0 {
		return 0
	}
	return float64(histogram.Max()) / float64(time.Millisecond)
}
func printJSON(value interface{}) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Println(string(encoded))
	return err
}
