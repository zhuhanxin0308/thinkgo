package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"runtime/pprof"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"thinkgo/framework"
	fwcontext "thinkgo/framework/context"
	"thinkgo/framework/db"
	fwhttp "thinkgo/framework/http"
)

const (
	productCount  = 100000
	customerCount = 50000
	orderCount    = 100000
)

var checkoutSequence atomic.Int64

func main() {
	mode := flag.String("mode", "serve", "prepare, serve, native, or load")
	addr := flag.String("addr", "127.0.0.1:18081", "HTTP listen address")
	baseURL := flag.String("base-url", "http://127.0.0.1:18081", "benchmark service URL")
	duration := flag.Duration("duration", 30*time.Second, "load-test duration")
	concurrency := flag.Int("concurrency", 32, "number of concurrent HTTP workers")
	loadScenario := flag.String("scenario", "mixed", "mixed, health, product, catalog, order, or checkout")
	logBuffer := flag.Int("log-buffer", 0, "access-log async buffer for control experiments; 0 keeps framework default")
	accessLog := flag.Bool("access-log", true, "write per-request access logs")
	cpuProfile := flag.String("cpu-profile", "", "optional CPU profile output path for the framework server")
	cpuProfileDuration := flag.Duration("cpu-profile-duration", 15*time.Second, "CPU profile duration")
	flag.Parse()

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
		fmt.Fprintln(os.Stderr, "mode must be prepare, serve, native, or load")
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
	app := framework.NewConsoleApp()
	if err := app.Initialize(); err != nil {
		return nil, fmt.Errorf("benchmark application initialization failed: %w", err)
	}
	config := db.Config{
		Type:                   "mysql",
		Hostname:               envOr(app, "DB_HOST", "127.0.0.1"),
		Hostport:               envOr(app, "DB_PORT", "3306"),
		Username:               envOr(app, "DB_USER", "root"),
		Password:               app.Env.Get("DB_PASS"),
		Database:               envOr(app, "DB_NAME", "thinkgo_benchmark"),
		Charset:                "utf8mb4",
		AutoTimestamp:          true,
		CreateTimeField:        "create_time",
		UpdateTimeField:        "update_time",
		TimestampValueType:     envOr(app, "DB_TIMESTAMP_VALUE_TYPE", "unix"),
		MaxOpenConns:           envInt(app, "DB_MAX_OPEN_CONNS", 512),
		MaxIdleConns:           envInt(app, "DB_MAX_IDLE_CONNS", 128),
		ConnMaxLifetimeSeconds: envInt(app, "DB_CONN_MAX_LIFETIME_SECONDS", 300),
		ConnMaxIdleTimeSeconds: envInt(app, "DB_CONN_MAX_IDLE_TIME_SECONDS", 60),
		Params:                 map[string]string{"tls": "skip-verify"},
	}
	database, err := db.Connect(config)
	if err != nil {
		_ = app.Close()
		return nil, fmt.Errorf("benchmark database connection failed: %w", err)
	}
	database.SetLogger(app.Log)
	manager := db.NewManager("mysql")
	if err := manager.Add("mysql", database); err != nil {
		_ = database.Close()
		_ = app.Close()
		return nil, err
	}
	app.DBManager = manager
	app.DB = database
	return app, nil
}

func envOr(app *framework.App, key, fallback string) string {
	if app != nil && app.Env != nil {
		if value := app.Env.Get(key); value != "" {
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

func prepare(app *framework.App) error {
	if app == nil || app.DB == nil {
		return errors.New("benchmark database is unavailable")
	}
	statements := []string{
		"DROP TABLE IF EXISTS bench_order_items",
		"DROP TABLE IF EXISTS bench_orders",
		"DROP TABLE IF EXISTS bench_products",
		"DROP TABLE IF EXISTS bench_customers",
		"CREATE TABLE bench_customers (id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, email VARCHAR(120) NOT NULL, name VARCHAR(100) NOT NULL, tier TINYINT UNSIGNED NOT NULL, created_at DATETIME NOT NULL, PRIMARY KEY (id), UNIQUE KEY uq_email (email), KEY idx_tier_id (tier, id)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
		"CREATE TABLE bench_products (id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, sku VARCHAR(40) NOT NULL, name VARCHAR(160) NOT NULL, category_id INT UNSIGNED NOT NULL, price DECIMAL(10,2) NOT NULL, stock INT UNSIGNED NOT NULL, created_at DATETIME NOT NULL, update_time BIGINT NOT NULL DEFAULT 0, create_time BIGINT NOT NULL DEFAULT 0, PRIMARY KEY (id), UNIQUE KEY uq_sku (sku), KEY idx_category_id (category_id, id)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
		"CREATE TABLE bench_orders (id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, order_no VARCHAR(48) NOT NULL, customer_id BIGINT UNSIGNED NOT NULL, status TINYINT UNSIGNED NOT NULL, total_amount DECIMAL(12,2) NOT NULL, created_at DATETIME NOT NULL, update_time BIGINT NOT NULL DEFAULT 0, create_time BIGINT NOT NULL DEFAULT 0, PRIMARY KEY (id), UNIQUE KEY uq_order_no (order_no), KEY idx_customer_status_created (customer_id, status, created_at), KEY idx_status_created (status, created_at)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
		"CREATE TABLE bench_order_items (id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, order_id BIGINT UNSIGNED NOT NULL, product_id BIGINT UNSIGNED NOT NULL, quantity INT UNSIGNED NOT NULL, unit_price DECIMAL(10,2) NOT NULL, create_time BIGINT NOT NULL DEFAULT 0, update_time BIGINT NOT NULL DEFAULT 0, PRIMARY KEY (id), KEY idx_order_id (order_id), KEY idx_product_id (product_id)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
		sequenceInsert("bench_customers", "id, email, name, tier, created_at", "n + 1, CONCAT('customer', n + 1, '@bench.local'), CONCAT('Customer ', n + 1), MOD(n, 20), NOW()", customerCount),
		sequenceInsert("bench_products", "id, sku, name, category_id, price, stock, created_at", "n + 1, CONCAT('SKU-', LPAD(n + 1, 8, '0')), CONCAT('Product ', n + 1), MOD(n, 200) + 1, ROUND(5 + MOD(n, 5000) * 0.17, 2), 1000, NOW()", productCount),
		sequenceInsert("bench_orders", "id, order_no, customer_id, status, total_amount, created_at", "n + 1, CONCAT('seed-', LPAD(n + 1, 10, '0')), MOD(n, 50000) + 1, MOD(n, 5), 0, TIMESTAMPADD(SECOND, -MOD(n, 2592000), NOW())", orderCount),
		"INSERT INTO bench_order_items (order_id, product_id, quantity, unit_price) SELECT o.id, MOD(o.id * 13 + d.n * 97, 100000) + 1, MOD(o.id + d.n, 5) + 1, ROUND(5 + MOD(o.id * 13 + d.n * 97, 5000) * 0.17, 2) FROM bench_orders o JOIN (SELECT 0 AS n UNION ALL SELECT 1 UNION ALL SELECT 2) d",
		"UPDATE bench_orders o JOIN (SELECT order_id, SUM(quantity * unit_price) AS total FROM bench_order_items GROUP BY order_id) i ON i.order_id = o.id SET o.total_amount = i.total",
	}
	for _, statement := range statements {
		if _, err := app.DB.Execute(statement); err != nil {
			return fmt.Errorf("prepare benchmark data: %w", err)
		}
	}
	rows, err := app.DB.Query("SELECT (SELECT COUNT(*) FROM bench_customers) AS customers, (SELECT COUNT(*) FROM bench_products) AS products, (SELECT COUNT(*) FROM bench_orders) AS orders, (SELECT COUNT(*) FROM bench_order_items) AS items")
	if err != nil {
		return err
	}
	return printJSON(map[string]interface{}{"prepared": true, "rows": rows[0]})
}

func sequenceInsert(table, fields, values string, count int) string {
	return fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM (SELECT a.n + 10 * b.n + 100 * c.n + 1000 * d.n + 10000 * e.n AS n FROM (SELECT 0 AS n UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4 UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9) a CROSS JOIN (SELECT 0 AS n UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4 UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9) b CROSS JOIN (SELECT 0 AS n UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4 UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9) c CROSS JOIN (SELECT 0 AS n UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4 UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9) d CROSS JOIN (SELECT 0 AS n UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4 UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9) e) sequence_data WHERE n < %d", table, fields, values, count)
}

func serve(app *framework.App, addr string, logBuffer int, accessLog bool, cpuProfilePath string, cpuProfileDuration time.Duration) error {
	if app == nil || app.DB == nil {
		return errors.New("benchmark database is unavailable")
	}
	if logBuffer > 0 && app.Log != nil {
		if err := app.Log.SetBufferSize(logBuffer); err != nil {
			return fmt.Errorf("set benchmark log buffer: %w", err)
		}
		if err := app.Log.SetFlushInterval(time.Second); err != nil {
			return fmt.Errorf("set benchmark log flush interval: %w", err)
		}
	}
	if !accessLog && app.Log != nil {
		app.Log.SetLevels([]string{"warning", "error"})
	}
	if cpuProfilePath != "" {
		if cpuProfileDuration <= 0 {
			return errors.New("CPU profile duration must be positive")
		}
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
	if _, err := app.Route.Get("/bench/health", func(req *fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Json(map[string]interface{}{"ok": true})
	}); err != nil {
		return err
	}
	if _, err := app.Route.Get("/bench/products/:id", func(req *fwcontext.Request) *fwcontext.Response {
		id := req.ParamInt64("id", 0)
		row, err := app.DB.Table("bench_products").WithContext(req.Raw().Context()).WhereField("id", "=", id).Field("id,sku,name,category_id,price,stock").Find()
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
	if _, err := app.Route.Get("/bench/catalog", func(req *fwcontext.Request) *fwcontext.Response {
		categoryID := boundedInt(req.Get("category_id", "1"), 1, 200, 1)
		page := boundedInt(req.Get("page", "1"), 1, 1000, 1)
		rows, err := app.DB.Table("bench_products").WithContext(req.Raw().Context()).WhereField("category_id", "=", categoryID).Field("id,sku,name,price,stock").Order("id DESC").Page(page, 20).Select()
		if err != nil {
			return dbError(err)
		}
		return fwcontext.NewResponse().Json(map[string]interface{}{"category_id": categoryID, "page": page, "products": rows})
	}); err != nil {
		return err
	}
	if _, err := app.Route.Get("/bench/orders/:id", func(req *fwcontext.Request) *fwcontext.Response {
		id := req.ParamInt64("id", 0)
		rows, err := app.DB.QueryContext(req.Raw().Context(), "SELECT o.id, o.order_no, o.status, o.total_amount, o.created_at, c.id AS customer_id, c.name AS customer_name, COUNT(oi.id) AS item_count, SUM(oi.quantity) AS item_quantity FROM bench_orders o JOIN bench_customers c ON c.id = o.customer_id JOIN bench_order_items oi ON oi.order_id = o.id WHERE o.id = ? GROUP BY o.id, o.order_no, o.status, o.total_amount, o.created_at, c.id, c.name", id)
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
	if _, err := app.Route.Post("/bench/checkout", func(req *fwcontext.Request) *fwcontext.Response {
		sequence := checkoutSequence.Add(1)
		customerID := sequence%customerCount + 1
		productID := sequence%productCount + 1
		var orderID int64
		err := app.DB.TransactionContext(req.Raw().Context(), func(tx *db.Tx) error {
			product, err := tx.Table("bench_products").WhereField("id", "=", productID).Lock().Find()
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
			if _, err = tx.Table("bench_products").WhereField("id", "=", productID).WhereField("stock", ">=", 1).Dec("stock", 1).Update(nil); err != nil {
				return err
			}
			orderID, err = tx.Table("bench_orders").Insert(map[string]interface{}{"order_no": fmt.Sprintf("load-%d", time.Now().UnixNano()+sequence), "customer_id": customerID, "status": 1, "total_amount": price, "created_at": time.Now()})
			if err != nil {
				return err
			}
			_, err = tx.Table("bench_order_items").Insert(map[string]interface{}{"order_id": orderID, "product_id": productID, "quantity": 1, "unit_price": price})
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

type workerStats struct {
	total, failures int64
	latencies       []time.Duration
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
	if loadScenario != "mixed" && loadScenario != "health" && loadScenario != "product" && loadScenario != "catalog" && loadScenario != "order" && loadScenario != "checkout" {
		return errors.New("scenario must be mixed, health, product, catalog, order, or checkout")
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
			rng := rand.New(rand.NewPCG(seed, seed+1))
			local := workerStats{latencies: make([]time.Duration, 0, 1024)}
			for time.Now().Before(deadline) {
				method, url := scenario(baseURL, rng, loadScenario)
				request, err := http.NewRequest(method, url, nil)
				if err != nil {
					local.failures++
					continue
				}
				started := time.Now()
				response, err := client.Do(request)
				elapsed := time.Since(started)
				local.total++
				local.latencies = append(local.latencies, elapsed)
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
	all := make([]time.Duration, 0)
	summary := result{Scenario: loadScenario, DurationSeconds: duration.Seconds(), Concurrency: concurrency}
	for local := range stats {
		summary.Requests += local.total
		summary.Failures += local.failures
		all = append(all, local.latencies...)
	}
	if len(all) == 0 {
		return errors.New("load test completed without requests")
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	summary.RPS = float64(summary.Requests) / duration.Seconds()
	summary.P50MS = latencyAt(all, 0.50)
	summary.P95MS = latencyAt(all, 0.95)
	summary.P99MS = latencyAt(all, 0.99)
	summary.MaxMS = float64(all[len(all)-1].Microseconds()) / 1000
	return printJSON(summary)
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

func latencyAt(latencies []time.Duration, percentile float64) float64 {
	index := int(float64(len(latencies)-1) * percentile)
	return float64(latencies[index].Microseconds()) / 1000
}
func printJSON(value interface{}) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Println(string(encoded))
	return err
}
