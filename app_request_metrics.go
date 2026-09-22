package framework

import "fmt"

// requestTaskPrometheus 暴露尚未完成真实收尾的请求，便于监控容量耗尽和停机超时。
func (app *App) requestTaskPrometheus() string {
	snapshot := app.RequestTaskSnapshot()
	return fmt.Sprintf(`# HELP thinkgo_request_tasks_active Requests whose execution or cleanup is still active.
# TYPE thinkgo_request_tasks_active gauge
thinkgo_request_tasks_active %d
# HELP thinkgo_request_tasks_operational_active Active operational requests including cleanup.
# TYPE thinkgo_request_tasks_operational_active gauge
thinkgo_request_tasks_operational_active %d
# HELP thinkgo_request_tasks_completed_total Fully completed request lifecycles.
# TYPE thinkgo_request_tasks_completed_total counter
thinkgo_request_tasks_completed_total %d
# HELP thinkgo_request_tasks_rejected_total Rejected request lifecycle reservations.
# TYPE thinkgo_request_tasks_rejected_total counter
thinkgo_request_tasks_rejected_total %d
# HELP thinkgo_request_tasks_shutdown_timeouts_total Shutdown waits that exceeded their budget.
# TYPE thinkgo_request_tasks_shutdown_timeouts_total counter
thinkgo_request_tasks_shutdown_timeouts_total %d
`, snapshot.Active, snapshot.Operational, snapshot.Completed, snapshot.Rejected, snapshot.ShutdownTimeouts)
}
