package testkit

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/binding"
	"github.com/zhuhanxin0308/thinkgo/v3/db"
	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
	"github.com/zhuhanxin0308/thinkgo/v3/openapi"
)

type testService struct{ Name string }
type testInput struct {
	binding.Input
	Name string `json:"name" validate:"required"`
}
type testOutput struct {
	Name string `json:"name"`
}

// TestHostRunsRealLifecycleAndDependencyReplacement 验证隔离应用完整启动、类型注入和配置覆盖。
func TestHostRunsRealLifecycleAndDependencyReplacement(t *testing.T) {
	host := New(t, Options{
		Config: map[string]any{"app.default_timezone": "UTC"},
		Register: func(app *framework.App) error {
			return app.RegisterRouteLoader(func(app *framework.App) error {
				registry, _ := openapi.NewRegistry(openapi3.Info{Title: "测试", Version: "1"})
				return openapi.Handle(app.Route(), registry, openapi.Operation{Method: http.MethodPost, Path: "/users", OperationID: "create", SuccessStatus: http.StatusCreated}, func(input testInput, service *testService) testOutput {
					return testOutput{Name: service.Name + input.Name}
				})
			})
		},
		Configure: func(app *framework.App) error {
			return app.Instance("testService", &testService{Name: "test-"})
		},
	})
	if host.App().Location().String() != "UTC" || !host.App().Initialized() {
		t.Fatal("测试宿主未应用配置或完成初始化")
	}
	response, err := host.JSON(http.MethodPost, "/users", testOutput{Name: "Ada"})
	if err != nil || response.Code != http.StatusCreated {
		t.Fatalf("请求失败: %#v %v", response, err)
	}
	output, err := DecodeJSON[testOutput](response)
	if err != nil || output.Name != "test-Ada" {
		t.Fatalf("依赖或 JSON 错误: %#v %v", output, err)
	}
	const requests = 12
	var wait sync.WaitGroup
	for range requests {
		wait.Go(func() {
			response, err := host.JSON(http.MethodPost, "/users", testOutput{Name: "并发"})
			if err != nil || response.Code != http.StatusCreated {
				t.Errorf("并发请求失败: %#v %v", response, err)
			}
		})
	}
	wait.Wait()
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	if response, err := host.JSON(http.MethodPost, "/users", nil); err != nil || response.Code < http.StatusInternalServerError {
		t.Fatalf("关闭后仍然执行业务: %#v %v", response, err)
	}
}

// TestHostReportsStartupFailures 验证初始化各阶段的错误可定位且不会交付半初始化宿主。
func TestHostReportsStartupFailures(t *testing.T) {
	failure := errors.New("测试启动失败")
	for _, options := range []Options{
		{Config: map[string]any{"app.server.port": "invalid"}},
		{Config: map[string]any{"../outside": true}},
		{Register: func(*framework.App) error { return failure }},
		{Configure: func(*framework.App) error { return failure }},
		{Register: func(app *framework.App) error {
			return app.RegisterRouteLoader(func(*framework.App) error { return failure })
		}},
	} {
		host, err := newHost(t.TempDir(), options)
		if err == nil || host != nil {
			t.Fatalf("错误配置交付了宿主: %#v %v", host, err)
		}
	}
}

// TestHostRequestPreservesContextAndRejectsInvalidInput 验证原始请求与 JSON 便捷入口的边界。
func TestHostRequestPreservesContextAndRejectsInvalidInput(t *testing.T) {
	host := New(t, Options{})
	if _, err := host.Do(nil); err == nil {
		t.Fatal("空请求未拒绝")
	}
	if _, err := host.JSON(http.MethodPost, "/", make(chan int)); err == nil {
		t.Fatal("不可编码请求未拒绝")
	}
	if _, err := host.JSON("invalid method", "/", nil); err == nil {
		t.Fatal("非法方法未拒绝")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
	if _, err := host.Do(request); !errors.Is(err, context.Canceled) {
		t.Fatalf("请求取消未保留: %v", err)
	}
	var absent *Host
	if _, err := absent.Do(request); err == nil {
		t.Fatal("空宿主未拒绝")
	}
	if err := absent.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestDecodeJSONValidatesContentAndPreservesNumbers 验证响应类型错误与多值 JSON 不会静默通过。
func TestDecodeJSONValidatesContentAndPreservesNumbers(t *testing.T) {
	for _, test := range []struct{ content, media string }{{`{}`, "text/html"}, {`{`, "application/json"}, {`{} {}`, "application/json"}} {
		response := httptest.NewRecorder()
		response.Header().Set("Content-Type", test.media)
		response.Body.WriteString(test.content)
		if _, err := DecodeJSON[testOutput](response); err == nil {
			t.Fatalf("非法 JSON 响应未拒绝: %#v", test)
		}
	}
	response := httptest.NewRecorder()
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.Body.WriteString(`{"id":9007199254740993}`)
	value, err := DecodeJSON[map[string]any](response)
	if err != nil || value["id"] != json.Number("9007199254740993") || !strings.Contains(response.Body.String(), "9007199254740993") {
		t.Fatalf("JSON 数字精度或响应体被改变: %#v %v", value, err)
	}
	if _, err := DecodeJSON[testOutput](nil); err == nil {
		t.Fatal("空响应未拒绝")
	}
}

type closeTrackingConnector struct {
	closed *atomic.Int32
}

func (c closeTrackingConnector) Open(string) (driver.Conn, error) {
	return closeTrackingConn(c), nil
}

func (c closeTrackingConnector) Connect(context.Context) (driver.Conn, error) {
	return closeTrackingConn(c), nil
}

func (c closeTrackingConnector) Driver() driver.Driver { return c }

type closeTrackingConn struct {
	closed *atomic.Int32
}

func (c closeTrackingConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unsupported")
}

func (c closeTrackingConn) Close() error {
	c.closed.Add(1)
	return nil
}

func (c closeTrackingConn) Begin() (driver.Tx, error) {
	return nil, errors.New("unsupported")
}

func newTrackedTestDatabase(t *testing.T) (*db.DB, *atomic.Int32) {
	t.Helper()
	closed := &atomic.Int32{}
	raw := sql.OpenDB(closeTrackingConnector{closed: closed})
	conn, err := raw.Conn(context.Background())
	if err != nil {
		t.Fatalf("打开跟踪连接失败: %v", err)
	}
	_ = conn.Close()
	return db.NewDB(db.NewSQLConnection(raw, &builder.Sqlite{})), closed
}

// 构造器交付宿主前必须在原协程退出时关闭已接收资源，退出原因对调用者保持可见。
func TestHostClosesOwnedResourcesOnAbnormalExit(t *testing.T) {
	for _, stage := range []string{"register", "configure"} {
		for _, mode := range []string{"panic", "goexit"} {
			t.Run(stage+"/"+mode, func(t *testing.T) {
				database, closed := newTrackedTestDatabase(t)
				t.Cleanup(func() { _ = database.Close() })
				marker := errors.New("装配失败")
				var app *framework.App
				callback := func(current *framework.App) error {
					app = current
					if mode == "panic" {
						panic(marker)
					}
					runtime.Goexit()
					return nil
				}
				opts := Options{Database: database}
				if stage == "register" {
					opts.Register = callback
				} else {
					opts.Configure = callback
				}
				var recovered any
				var returned bool
				done := make(chan struct{})
				go func() {
					defer close(done)
					defer func() { recovered = recover() }()
					New(t, opts)
					returned = true
				}()
				<-done
				if returned || (mode == "panic" && recovered != marker) || (mode == "goexit" && recovered != nil) {
					t.Fatalf("构造异常未保留：返回=%t，异常=%v", returned, recovered)
				}
				if closed.Load() != 1 || app == nil || app.State() != framework.ApplicationStateClosed {
					t.Fatalf("未交付的宿主没有完成清理：数据库关闭次数=%d，应用=%v", closed.Load(), app != nil)
				}
			})
		}
	}
}

// TestHostRunsAsHTTPApplicationAndFreezesContainer 验证 testkit.New 以非控制台 HTTP 模式运行、
// 持有 RunLease 冻结运行期容器、执行真实数据库策略并在早退时关闭 Options.Database。
func TestHostRunsAsHTTPApplicationAndFreezesContainer(t *testing.T) {
	host := New(t, Options{
		Config: map[string]any{
			"app":           map[string]any{"app_debug": false},
			"App.app_debug": true,
		},
		Register: func(app *framework.App) error {
			return app.RegisterRouteLoader(func(app *framework.App) error {
				app.Route().Get("/ping", func() map[string]any {
					return map[string]any{
						"console": app.RunningInConsole(),
						"debug":   app.IsDebug(),
					}
				})
				return nil
			})
		},
	})
	if host.App().RunningInConsole() {
		t.Fatal("HTTP 测试宿主不应处于 RunningInConsole 模式")
	}
	if host.App().State() != framework.ApplicationStateRunning {
		t.Fatalf("测试宿主应持有运行租约处于 running 状态，实际为 %s", host.App().State())
	}
	if !host.App().IsDebug() {
		t.Fatal("规范化路径深度排序应保留子路径 App.app_debug=true 覆盖")
	}
	if err := host.App().Instance("late_mutated_service", "forbidden"); !errors.Is(err, framework.ErrApplicationRunning) {
		t.Fatalf("运行期容器必须拒绝滞后 Instance 注入: %v", err)
	}
	response, err := host.JSON(http.MethodGet, "/ping", nil)
	if err != nil || response.Code != http.StatusOK {
		t.Fatalf("合并默认 server 配置后的请求应成功: code=%d err=%v body=%s", response.Code, err, response.Body.String())
	}

	// required 数据库策略缺少配置时必须在启动阶段失败（证明未跳过数据库 Provider）。
	if gotHost, err := newHost(t.TempDir(), Options{
		Config: map[string]any{"app.database_startup_policy": "required"},
	}); err == nil || gotHost != nil {
		t.Fatalf("required 数据库策略缺少配置应启动失败: host=%#v err=%v", gotHost, err)
	}

	// 原生多应用子应用注册错误必须在 newHost 预热阶段立即暴露，而非延迟到首个请求。
	multiDir := t.TempDir()
	for _, sub := range []string{"index", "admin"} {
		if err := os.MkdirAll(filepath.Join(multiDir, "app", sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if gotHost, err := newHost(multiDir, Options{
		Register: func(app *framework.App) error {
			return app.RegisterApplications(func(*framework.App) error { return nil },
				framework.ApplicationDefinition{Name: "index", Register: func(*framework.App) error { return nil }},
				framework.ApplicationDefinition{Name: "admin", Register: func(*framework.App) error { return errors.New("admin broken") }},
			)
		},
	}); err == nil || gotHost != nil || !strings.Contains(err.Error(), "admin broken") {
		t.Fatalf("原生多应用子应用错误应在 newHost 阶段失败: host=%#v err=%v", gotHost, err)
	}

	// 早退失败（配置错误 / Register 错误 / Configure 错误）必须兜底关闭传入的 Options.Database。
	for _, step := range []string{"config", "register", "configure"} {
		database, closed := newTrackedTestDatabase(t)
		opts := Options{Database: database}
		switch step {
		case "config":
			opts.Config = map[string]any{"app.app_debug": false, "App.app_debug": true}
		case "register":
			opts.Register = func(*framework.App) error { return errors.New("register boom") }
		case "configure":
			opts.Configure = func(*framework.App) error { return errors.New("configure boom") }
		}
		if gotHost, err := newHost(t.TempDir(), opts); err == nil || gotHost != nil {
			t.Fatalf("早退场景 %s 应失败: host=%#v err=%v", step, gotHost, err)
		}
		if closed.Load() == 0 {
			t.Fatalf("早退场景 %s 未关闭传入的 Options.Database，发生连接泄漏", step)
		}
	}
}

// TestHostJSONSetsSimulatedClientRemoteAddrForRateLimit 验证启用默认客户端 IP 限流时，
// Host.JSON 与 Host.Do(httptest.NewRequest) 行为一致，同时保留生产空 RemoteAddr 失败关闭策略。
func TestHostJSONSetsSimulatedClientRemoteAddrForRateLimit(t *testing.T) {
	host := New(t, Options{
		Config: map[string]any{
			"rate_limit.enable": true,
			"rate_limit.rate":   1,
			"rate_limit.burst":  2,
		},
		Register: func(app *framework.App) error {
			return app.RegisterRouteLoader(func(app *framework.App) error {
				app.Route().Get("/ok", func() map[string]string {
					return map[string]string{"status": "ok"}
				})
				return nil
			})
		},
	})

	first, err := host.JSON(http.MethodGet, "/ok", nil)
	if err != nil || first.Code != http.StatusOK {
		t.Fatalf("首次未超额 JSON 请求应返回 200，实际 code=%d err=%v body=%s", first.Code, err, first.Body.String())
	}
	if first.Header().Get("RateLimit-Limit") == "" {
		t.Fatalf("限流启用后应返回 RateLimit-* 响应头: %#v", first.Header())
	}

	inbound := httptest.NewRequest(http.MethodGet, "http://example.com/ok", nil)
	second, err := host.Do(inbound)
	if err != nil || second.Code != http.StatusOK {
		t.Fatalf("同客户端第二次请求仍在额度内应返回 200，实际 code=%d err=%v", second.Code, err)
	}

	third, err := host.JSON(http.MethodGet, "/ok", nil)
	if err != nil || third.Code != http.StatusTooManyRequests {
		t.Fatalf("超出额度后应返回 429，实际 code=%d err=%v", third.Code, err)
	}

	// 生产 ClientIPRateLimitKey 收到缺失 RemoteAddr 的原始请求时仍保持 400 失败关闭，不信任伪造 XFF。
	rawMissingAddr, err := http.NewRequest(http.MethodGet, "http://example.com/ok", nil)
	if err != nil {
		t.Fatal(err)
	}
	rawMissingAddr.Header.Set("X-Forwarded-For", "198.51.100.88")
	rejected, err := host.Do(rawMissingAddr)
	if err != nil || rejected.Code != http.StatusBadRequest {
		t.Fatalf("缺失 RemoteAddr 的原始请求仍应 400 失败关闭: code=%d err=%v", rejected.Code, err)
	}
}
