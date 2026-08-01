package framework

import (
	stdcontext "context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	fwcontext "thinkgo/framework/context"
	"thinkgo/framework/health"
	"thinkgo/framework/metrics"
	"thinkgo/framework/route"
)

// TestDatabaseReadinessHidesInternalError 验证数据库故障进入 readiness，但不会被探针 JSON 泄露。
func TestDatabaseReadinessHidesInternalError(t *testing.T) {
	app := &App{health: health.NewRegistry()}
	app.setDatabaseReadinessError(errors.New("mysql password=secret"))
	app.registerDatabaseReadinessCheck()

	report := app.health.Readiness(stdcontext.Background())
	if report.Healthy() || report.HTTPStatus() != http.StatusServiceUnavailable {
		t.Fatalf("数据库不可用时 readiness 必须失败: %#v", report)
	}
	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("序列化 readiness 报告失败: %v", err)
	}
	if strings.Contains(string(payload), "secret") {
		t.Fatalf("readiness JSON 不得包含底层错误: %s", payload)
	}

	app.setDatabaseReadinessError(nil)
	if report = app.health.Readiness(stdcontext.Background()); !report.Healthy() {
		t.Fatalf("数据库恢复后 readiness 应通过: %#v", report)
	}
}

// TestOperationalRoutesUseSafeFrameworkHandlers 验证运维端点由框架路由统一管理，且指标端点受开关控制。
func TestOperationalRoutesUseSafeFrameworkHandlers(t *testing.T) {
	app := &App{
		route:   route.NewRouter(),
		health:  health.NewRegistry(),
		metrics: metrics.NewRegistry(),
	}
	app.metricsEnabled = true
	app.metrics.Enable()
	app.registerOperationalRoutes()

	for _, path := range []string{OperationalLivenessPath, OperationalReadinessPath, OperationalMetricsPath} {
		request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com"+path, nil))
		matched, _, err := app.route.Match(request)
		if err != nil || matched == nil {
			t.Fatalf("运维端点 %q 未注册: route=%v err=%v", path, matched, err)
		}
		handler, ok := matched.Handler().(func(*fwcontext.Request) *fwcontext.Response)
		if !ok {
			t.Fatalf("运维端点 %q 的处理器类型不安全: %T", path, matched.Handler())
		}
		response := handler(request)
		if response == nil || response.GetStatus() != http.StatusOK {
			t.Fatalf("运维端点 %q 未返回成功响应: %#v", path, response)
		}
	}
}
