package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type pausedMetricsWriter struct {
	*httptest.ResponseRecorder
	once    sync.Once
	entered chan struct{}
	resume  chan struct{}
}

func (writer *pausedMetricsWriter) Header() http.Header {
	writer.once.Do(func() {
		close(writer.entered)
		<-writer.resume
	})
	return writer.ResponseRecorder.Header()
}

// 让关闭发生在成功抓取首次访问响应头后，验证已接受的响应包含完整指标。
func TestScrapeCompletesAcrossDisable(t *testing.T) {
	registry := NewRegistry()
	registry.Enable()
	registry.Observe(http.StatusOK, time.Millisecond)
	want := registry.Prometheus()
	writer := &pausedMetricsWriter{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), resume: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		registry.ServeHTTP(writer, nil)
	}()
	<-writer.entered
	registry.Disable()
	close(writer.resume)
	<-done
	if writer.Code != http.StatusOK || writer.Body.String() != want || writer.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("抓取返回不完整：状态=%d，正文=%q", writer.Code, writer.Body.String())
	}
}

func TestRegistryEnableObserveAndPrometheus(t *testing.T) {
	registry := NewRegistry()
	if registry.Enabled() {
		t.Fatal("指标注册表默认不应启用")
	}
	if got := registry.Prometheus(); got != "" {
		t.Fatalf("未启用指标时不应导出内容: %q", got)
	}
	registry.Enable()
	if !registry.Begin() {
		t.Fatal("启用指标后 Begin 应成功")
	}
	registry.End(true, http.StatusOK, 2*time.Millisecond)
	registry.Observe(http.StatusCreated, 10*time.Millisecond)
	registry.Observe(http.StatusNotFound, 60*time.Millisecond)
	registry.Observe(http.StatusInternalServerError, 2*time.Second)

	snapshot := registry.Snapshot()
	if snapshot.Requests != 4 || snapshot.Errors != 1 || snapshot.InFlight != 0 {
		t.Fatalf("基础指标错误: %#v", snapshot)
	}
	if snapshot.StatusClasses[1] != 2 || snapshot.StatusClasses[4] != 1 {
		t.Fatalf("状态类别指标错误: %#v", snapshot.StatusClasses)
	}
	if snapshot.DurationCount != 4 || snapshot.DurationSum <= 2 {
		t.Fatalf("耗时指标错误: %#v", snapshot)
	}
	output := registry.Prometheus()
	for _, fragment := range []string{
		"# TYPE thinkgo_http_requests_total counter",
		"thinkgo_http_requests_total 4",
		"thinkgo_http_errors_total 1",
		"thinkgo_http_status_class_requests_total{class=\"2xx\"} 2",
		"thinkgo_http_request_duration_seconds_bucket{le=\"+Inf\"} 4",
		"thinkgo_http_request_duration_seconds_count 4",
	} {
		if !strings.Contains(output, fragment) {
			t.Fatalf("Prometheus 输出缺少 %q:\n%s", fragment, output)
		}
	}
}

func TestRegistryCountsUnknownAndDeclaredHijackedResponses(t *testing.T) {
	registry := NewRegistry()
	registry.EndHijacked(registry.Begin(), 0, time.Millisecond)
	registry.Enable()
	registry.EndHijacked(registry.Begin(), 0, time.Millisecond)
	registry.EndHijacked(registry.Begin(), http.StatusSwitchingProtocols, time.Millisecond)
	snapshot := registry.Snapshot()
	if snapshot.Requests != 2 || snapshot.InFlight != 0 || snapshot.OtherStatuses != 1 ||
		snapshot.HijackedUnknown != 1 || snapshot.HijackedKnown != 1 || snapshot.StatusClasses[0] != 1 ||
		snapshot.StatusClasses[1] != 0 {
		t.Fatalf("接管计数或状态分类错误: %+v", snapshot)
	}
	for _, fragment := range []string{
		`thinkgo_http_hijacked_requests_total{status_known="false"} 1`,
		`thinkgo_http_hijacked_requests_total{status_known="true"} 1`,
	} {
		if !strings.Contains(registry.Prometheus(), fragment) {
			t.Errorf("接管指标输出缺少 %q", fragment)
		}
	}
	registry.Reset()
	if snapshot := registry.Snapshot(); snapshot.HijackedKnown != 0 || snapshot.HijackedUnknown != 0 {
		t.Fatalf("重置未清除接管计数: %+v", snapshot)
	}
}

func TestRegistryServeHTTPAndReset(t *testing.T) {
	registry := NewRegistry()
	recorder := httptest.NewRecorder()
	registry.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("未启用指标端点应返回 404，实际为 %d", recorder.Code)
	}
	registry.Enable()
	active := registry.Begin()
	registry.Observe(http.StatusNoContent, time.Millisecond)
	registry.Reset()
	snapshot := registry.Snapshot()
	if snapshot.InFlight != 1 || snapshot.Requests != 0 {
		t.Fatalf("Reset 不应破坏活跃请求，但应清理已完成指标: %#v", snapshot)
	}
	registry.End(active, http.StatusOK, time.Millisecond)
	recorder = httptest.NewRecorder()
	registry.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "thinkgo_http_requests_total 1") {
		t.Fatalf("启用指标端点输出错误: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("指标 Content-Type 错误: %q", got)
	}
}

func TestRegistryConcurrentObserve(t *testing.T) {
	registry := NewRegistry()
	registry.Enable()
	const workers = 8
	const observationsPerWorker = 200
	var waitGroup sync.WaitGroup
	waitGroup.Add(workers)
	for worker := 0; worker < workers; worker++ {
		go func() {
			defer waitGroup.Done()
			for index := 0; index < observationsPerWorker; index++ {
				registry.Observe(http.StatusOK, time.Duration(index)*time.Microsecond)
			}
		}()
	}
	waitGroup.Wait()
	if got := registry.Snapshot().Requests; got != workers*observationsPerWorker {
		t.Fatalf("并发指标丢失: got=%d want=%d", got, workers*observationsPerWorker)
	}
}
