package framework

import (
	"errors"
	"strings"
	"testing"
	"time"
)

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
