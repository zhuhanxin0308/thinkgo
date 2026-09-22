package health

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// nilHealthContext 保留健康检查对 nil 上下文的兼容边界测试，避免在业务代码中传递 nil。
func nilHealthContext() context.Context {
	return nil
}

func TestRegistryReadinessOrdersChecksAndHidesErrors(t *testing.T) {
	registry := NewRegistry()
	secret := errors.New("database password=secret")
	if err := registry.Register("z-cache", func(context.Context) error { return nil }); err != nil {
		t.Fatalf("注册成功检查失败: %v", err)
	}
	if err := registry.Register("a-database", func(context.Context) error { return secret }); err != nil {
		t.Fatalf("注册失败检查失败: %v", err)
	}
	report := registry.Readiness(context.Background())
	if report.Healthy() || report.Status != StatusFail || len(report.Checks) != 2 {
		t.Fatalf("就绪报告错误: %#v", report)
	}
	if report.Checks["a-database"].Error == nil || !errors.Is(report.Checks["a-database"].Error, secret) {
		t.Fatalf("失败原因应保留给进程内调用方: %#v", report.Checks["a-database"])
	}
	if report.Checks["a-database"].Status != StatusFail || report.Checks["z-cache"].Status != StatusOK {
		t.Fatalf("检查状态错误: %#v", report.Checks)
	}
	request := httptest.NewRequest(http.MethodGet, "/ready", nil)
	recorder := httptest.NewRecorder()
	registry.ReadinessHandler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("就绪失败应返回 503，实际为 %d", recorder.Code)
	}
	body := recorder.Body.String()
	if strings.Contains(body, "secret") || strings.Contains(body, "password") {
		t.Fatalf("健康端点不应暴露检查错误: %q", body)
	}
}

func TestRegistryLivenessAndTimeout(t *testing.T) {
	registry := NewRegistry()
	if report := registry.Liveness(); !report.Healthy() || report.HTTPStatus() != http.StatusOK {
		t.Fatalf("存活报告错误: %#v", report)
	}
	if err := registry.SetTimeout(10 * time.Millisecond); err != nil {
		t.Fatalf("设置健康检查超时失败: %v", err)
	}
	if err := registry.Register("slow", func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}); err != nil {
		t.Fatalf("注册慢检查失败: %v", err)
	}
	started := time.Now()
	report := registry.Readiness(context.Background())
	if report.Healthy() || report.Checks["slow"].Error == nil {
		t.Fatalf("超时检查应失败: %#v", report)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("健康检查超时未被限制: %s", time.Since(started))
	}
	livenessRecorder := httptest.NewRecorder()
	registry.LivenessHandler().ServeHTTP(livenessRecorder, httptest.NewRequest(http.MethodGet, "/live", nil))
	if livenessRecorder.Code != http.StatusOK || !strings.Contains(livenessRecorder.Body.String(), `"status":"ok"`) {
		t.Fatalf("存活端点错误: status=%d body=%q", livenessRecorder.Code, livenessRecorder.Body.String())
	}
}

// TestRegistryReadinessHardTimeoutsNonCooperativeCheck 验证检查函数忽略 Context 时，总预算仍能硬性结束本次就绪检查。
func TestRegistryReadinessHardTimeoutsNonCooperativeCheck(t *testing.T) {
	registry := NewRegistry()
	if err := registry.SetTimeout(20 * time.Millisecond); err != nil {
		t.Fatalf("设置健康检查超时失败: %v", err)
	}
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	if err := registry.Register("blocked", func(context.Context) error {
		<-release
		return nil
	}); err != nil {
		t.Fatalf("注册不协作检查失败: %v", err)
	}

	started := time.Now()
	report := registry.Readiness(context.Background())
	elapsed := time.Since(started)
	if report.Healthy() || !errors.Is(report.Checks["blocked"].Error, context.DeadlineExceeded) {
		t.Fatalf("不协作检查应按总超时失败: %#v", report)
	}
	if elapsed > 250*time.Millisecond {
		t.Fatalf("不协作检查突破总预算: %s", elapsed)
	}
}

func TestRegistryValidationAndPanicIsolation(t *testing.T) {
	registry := NewRegistry()
	for _, name := range []string{"", " leading", "trailing ", strings.Repeat("x", maxCheckName+1), "line\nfeed"} {
		if err := registry.Register(name, func(context.Context) error { return nil }); !errors.Is(err, ErrInvalidCheckName) {
			t.Fatalf("非法检查名称应被拒绝: name=%q err=%v", name, err)
		}
	}
	if err := registry.Register("panic", func(context.Context) error { panic("secret panic") }); err != nil {
		t.Fatalf("注册 panic 检查失败: %v", err)
	}
	if err := registry.Register("panic", func(context.Context) error { return nil }); !errors.Is(err, ErrInvalidCheck) {
		t.Fatalf("重复检查应被拒绝: %v", err)
	}
	report := registry.Readiness(context.Background())
	if report.Healthy() || report.Checks["panic"].Error == nil {
		t.Fatalf("panic 检查应隔离为失败: %#v", report)
	}
	if !registry.Unregister("panic") || registry.Unregister("panic") {
		t.Fatal("Unregister 返回值错误")
	}
	if err := registry.SetTimeout(0); !errors.Is(err, ErrInvalidTimeout) {
		t.Fatalf("非法超时应被拒绝: %v", err)
	}
}

func TestNilRegistryAndEmptyReadinessBoundaries(t *testing.T) {
	var registry *Registry
	if registry.Timeout() != 0 || registry.Unregister("missing") {
		t.Fatal("空注册表边界返回值错误")
	}
	if err := registry.SetTimeout(time.Second); !errors.Is(err, ErrInvalidTimeout) {
		t.Fatalf("空注册表设置超时应失败: %v", err)
	}
	if err := registry.Register("db", func(context.Context) error { return nil }); !errors.Is(err, ErrInvalidCheck) {
		t.Fatalf("空注册表注册检查应失败: %v", err)
	}
	if report := registry.Liveness(); report.HTTPStatus() != http.StatusServiceUnavailable {
		t.Fatalf("空注册表存活状态应失败: %#v", report)
	}
	if report := registry.Readiness(nilHealthContext()); report.HTTPStatus() != http.StatusServiceUnavailable {
		t.Fatalf("空注册表就绪状态应失败: %#v", report)
	}
	livenessRecorder := httptest.NewRecorder()
	registry.LivenessHandler().ServeHTTP(livenessRecorder, nil)
	if livenessRecorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("空注册表存活端点应返回 503: %d", livenessRecorder.Code)
	}
	readinessRecorder := httptest.NewRecorder()
	registry.ReadinessHandler().ServeHTTP(readinessRecorder, nil)
	if readinessRecorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("空注册表就绪端点应返回 503: %d", readinessRecorder.Code)
	}

	empty := NewRegistry()
	report := empty.Readiness(nilHealthContext())
	if !report.Healthy() || report.HTTPStatus() != http.StatusOK || len(report.Checks) != 0 {
		t.Fatalf("无检查项就绪报告错误: %#v", report)
	}
}
