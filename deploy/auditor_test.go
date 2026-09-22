package deploy

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/config"
)

func newDeployTestApp(t *testing.T) (*framework.App, *config.Config) {
	t.Helper()
	basePath := t.TempDir()
	app := framework.NewAppUninitialized(basePath)
	t.Cleanup(func() { _ = app.Close() })
	configuration, err := framework.ResolveServiceAs[*config.Config](app, framework.ServiceConfig)
	if err != nil {
		t.Fatalf("解析部署测试配置失败: %v", err)
	}
	return app, configuration
}

// TestAuditorSortsChecksAndIsolatesPanic 验证部署检查顺序稳定，单个 panic 不会跳过其它检查。
func TestAuditorSortsChecksAndIsolatesPanic(t *testing.T) {
	auditor, err := NewAuditor(
		checkFunc{name: "z.pass", run: func(context.Context, *framework.App) Result { return pass("ok") }},
		checkFunc{name: "a.panic", run: func(context.Context, *framework.App) Result { panic("broken") }},
	)
	if err != nil {
		t.Fatalf("创建部署审计器失败: %v", err)
	}
	report := auditor.Run(context.Background(), nil)
	if len(report.Results) != 2 || report.Results[0].Name != "a.panic" || report.Results[1].Name != "z.pass" {
		t.Fatalf("部署检查顺序错误: %#v", report.Results)
	}
	if report.Results[0].Level != LevelFail || report.Results[1].Level != LevelPass || !report.Failed(false) {
		t.Fatalf("部署检查 panic 隔离错误: %#v", report.Results)
	}
	if _, err := NewAuditor(
		checkFunc{name: "duplicate", run: func(context.Context, *framework.App) Result { return pass("ok") }},
		checkFunc{name: "duplicate", run: func(context.Context, *framework.App) Result { return pass("ok") }},
	); err == nil {
		t.Fatal("重复部署检查名称必须被拒绝")
	}
}

// TestAuditorBoundsContextIgnoringChecks 验证忽略 context 的扩展检查不能阻塞审计，
// 且重复超时不会创建无限后台 goroutine。
func TestAuditorBoundsContextIgnoringChecks(t *testing.T) {
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	auditor, err := NewAuditor(checkFunc{name: "blocking", run: func(context.Context, *framework.App) Result {
		<-stop
		return pass("released")
	}})
	if err != nil {
		t.Fatalf("创建阻塞检查审计器失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	report := auditor.Run(ctx, nil)
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("忽略 context 的检查仍阻塞审计: elapsed=%s", elapsed)
	}
	if len(report.Results) != 1 || report.Results[0].Level != LevelFail || !strings.Contains(report.Results[0].Message, "超时") {
		t.Fatalf("阻塞检查必须返回稳定超时失败: %#v", report.Results)
	}

	for index := 0; index < maximumOrphanedChecks; index++ {
		shortCtx, shortCancel := context.WithTimeout(context.Background(), time.Millisecond)
		if result := auditor.Run(shortCtx, nil).Results[0]; result.Level != LevelFail {
			t.Fatalf("第 %d 次超时检查必须失败: %#v", index, result)
		}
		shortCancel()
	}
	shortCtx, shortCancel := context.WithTimeout(context.Background(), time.Second)
	defer shortCancel()
	result := auditor.Run(shortCtx, nil).Results[0]
	if result.Level != LevelFail || !strings.Contains(result.Message, "槽") {
		t.Fatalf("孤儿检查达到上限后必须 fail-closed: %#v", result)
	}
}

// TestProductionAndHostChecksRejectUnsafeConfiguration 验证非生产、调试开关和通配 Host 都会阻断部署。
func TestProductionAndHostChecksRejectUnsafeConfiguration(t *testing.T) {
	app, configuration := newDeployTestApp(t)
	configuration.Set("app.app_env", "development")
	configuration.Set("app.app_debug", true)
	configuration.Set("app.server.allowed_hosts", []interface{}{"*"})
	if result := checkProductionConfiguration(context.Background(), app); result.Level != LevelFail {
		t.Fatalf("开发环境必须阻断部署: %#v", result)
	}
	if result := checkAllowedHosts(context.Background(), app); result.Level != LevelFail {
		t.Fatalf("通配 Host 必须阻断部署: %#v", result)
	}

	configuration.Set("app.app_env", "production")
	configuration.Set("app.app_debug", false)
	configuration.Set("app.app_trace", false)
	configuration.Set("app.security_profile", string(framework.SecurityProfileStatelessAPI))
	configuration.Set("app.server.allowed_hosts", []interface{}{"api.example.com"})
	if result := checkProductionConfiguration(context.Background(), app); result.Level != LevelPass {
		t.Fatalf("安全生产配置应通过: %#v", result)
	}
	if result := checkAllowedHosts(context.Background(), app); result.Level != LevelPass {
		t.Fatalf("显式 Host 应通过: %#v", result)
	}
}

// TestSecurityHeaderCheckRequiresExplicitProductionPolicy 验证浏览器安全响应头
// 必须显式启用；普通审计给出警告，严格发布门禁会据此阻断。
func TestSecurityHeaderCheckRequiresExplicitProductionPolicy(t *testing.T) {
	app, configuration := newDeployTestApp(t)
	if result := checkSecurityHeaders(context.Background(), app); result.Level != LevelWarn {
		t.Fatalf("未启用安全响应头必须产生警告: %#v", result)
	}
	configuration.Set("security_headers.enable", true)
	if result := checkSecurityHeaders(context.Background(), app); result.Level != LevelPass {
		t.Fatalf("显式启用安全响应头应通过: %#v", result)
	}
}

// TestRuntimeAndTLSChecksUseRealFilesystemState 验证运行时写入探针会清理，伪造的 TLS 文本不能通过门禁。
func TestRuntimeAndTLSChecksUseRealFilesystemState(t *testing.T) {
	app, configuration := newDeployTestApp(t)
	if err := os.MkdirAll(app.RuntimePath, 0o700); err != nil {
		t.Fatalf("创建部署测试运行时目录失败: %v", err)
	}
	if result := checkRuntimeFilesystem(context.Background(), app); result.Level != LevelPass {
		t.Fatalf("可写运行时目录应通过: %#v", result)
	}
	entries, err := os.ReadDir(app.RuntimePath)
	if err != nil || len(entries) != 0 {
		t.Fatalf("运行时写入探针必须清理: entries=%v err=%v", entries, err)
	}

	certPath := filepath.Join(app.BasePath, "cert.pem")
	keyPath := filepath.Join(app.BasePath, "key.pem")
	if err := os.WriteFile(certPath, []byte("certificate"), 0o644); err != nil {
		t.Fatalf("写入测试证书失败: %v", err)
	}
	if err := os.WriteFile(keyPath, []byte("private-key"), 0o600); err != nil {
		t.Fatalf("写入测试私钥失败: %v", err)
	}
	configuration.Set("app.server.tls.enable", true)
	configuration.Set("app.server.tls.cert_file", certPath)
	configuration.Set("app.server.tls.key_file", keyPath)
	result := checkTLSFiles(context.Background(), app)
	if result.Level != LevelFail {
		t.Fatalf("非 PEM 文本必须被 TLS 门禁拒绝: %#v", result)
	}
	if strings.Contains(result.Message, certPath) || strings.Contains(result.Message, keyPath) || strings.Contains(result.Message, "private-key") {
		t.Fatalf("TLS 门禁错误不得泄露路径或私钥内容: %#v", result)
	}
}

// TestRouteCheckLoadsRegisteredRoutes 验证部署审计会执行正式路由加载器，
// 不能把尚未触发首次请求的动态路由误报为纯静态应用。
func TestRouteCheckLoadsRegisteredRoutes(t *testing.T) {
	app, _ := newDeployTestApp(t)
	if err := app.RegisterRouteLoader(func(current *framework.App) error {
		current.Route().Get("/health", func() string { return "ok" })
		return nil
	}); err != nil {
		t.Fatalf("注册部署审计路由失败: %v", err)
	}
	result := checkRoutes(context.Background(), app)
	if result.Level != LevelPass || !strings.Contains(result.Message, "1 条") {
		t.Fatalf("部署审计未加载注册路由: %#v", result)
	}
}

// TestDefaultAuditorExposesStableCoverage 验证默认检查集合完整且严格模式会阻断警告。
func TestDefaultAuditorExposesStableCoverage(t *testing.T) {
	app, _ := newDeployTestApp(t)
	report := NewDefaultAuditor().Run(context.Background(), app)
	names := make([]string, len(report.Results))
	for index, result := range report.Results {
		names[index] = result.Name
	}
	want := []string{
		"application.lifecycle",
		"configuration.production",
		"database.connectivity",
		"database.startup_policy",
		"filesystem.runtime",
		"http.hosts",
		"http.operational_access",
		"http.security_headers",
		"http.tls",
		"migrations.status",
		"observability.telemetry",
		"routes.automatic_dispatch",
		"routes.snapshot",
	}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("默认部署检查集合错误: want=%v got=%v", want, names)
	}
	if !report.Failed(false) || !report.Failed(true) {
		t.Fatalf("未初始化测试应用必须阻断部署: %#v", report.Results)
	}
	warningOnly := Report{Results: []Result{{Name: "warning", Level: LevelWarn, Message: "review"}}}
	if warningOnly.Failed(false) || !warningOnly.Failed(true) {
		t.Fatal("严格模式必须把警告升级为部署阻断")
	}
}
