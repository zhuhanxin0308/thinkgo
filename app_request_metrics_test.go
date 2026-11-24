package framework

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/metrics"
)

func TestOperationalMetricsHonorsRegistryState(t *testing.T) {
	app := &App{operationalAccess: OperationalAccessPublic}
	registry := metrics.NewRegistry()
	registry.Enable()
	registry.Observe(http.StatusServiceUnavailable, time.Millisecond)
	response := app.metricsRouteWithRegistry(registry, nil)
	if response.GetStatus() != http.StatusOK || response.GetHeader("Cache-Control") != "no-store" ||
		string(response.GetBody()) != registry.Prometheus()+app.requestTaskPrometheus() {
		t.Fatal("公开运维端点没有返回完整的 HTTP 与任务指标")
	}
	registry.Disable()
	response = app.metricsRouteWithRegistry(registry, nil)
	if response.GetStatus() != http.StatusNotFound || strings.Contains(string(response.GetBody()), "thinkgo_") {
		t.Fatal("关闭采集后仍输出了部分指标")
	}
}

// TestRequestTaskMetricsReflectActualCleanup 验证指标区分真实在途任务、运维请求、拒绝和完成。
func TestRequestTaskMetricsReflectActualCleanup(t *testing.T) {
	app := &App{}
	request, err := app.AcquireRequestTask(1, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer request.Release()
	operational, err := app.AcquireOperationalRequestTask(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer operational.Release()
	if _, err := app.AcquireRequestTask(1, time.Second); !errors.Is(err, ErrRequestTaskCapacity) {
		t.Fatalf("容量满时应拒绝业务请求: %v", err)
	}
	for _, expected := range []string{
		"thinkgo_request_tasks_active 2\n",
		"thinkgo_request_tasks_operational_active 1\n",
		"thinkgo_request_tasks_completed_total 0\n",
		"thinkgo_request_tasks_rejected_total 1\n",
		"thinkgo_request_tasks_shutdown_timeouts_total 0\n",
	} {
		if !strings.Contains(app.requestTaskPrometheus(), expected) {
			t.Fatalf("缺少当前生命周期指标 %q", expected)
		}
	}
	request.Release()
	operational.Release()
	if output := app.requestTaskPrometheus(); !strings.Contains(output, "thinkgo_request_tasks_active 0\n") || !strings.Contains(output, "thinkgo_request_tasks_completed_total 2\n") {
		t.Fatalf("真实清理完成后指标未归还容量: %s", output)
	}
}
