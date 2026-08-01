// Package metrics 提供无需外部依赖的进程内 HTTP 指标采集能力。
package metrics

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	statusClassCount    = 5
	durationBucketCount = 12
	bucketCount         = durationBucketCount + 1
)

var durationBucketBounds = [durationBucketCount]float64{
	0.001,
	0.005,
	0.01,
	0.025,
	0.05,
	0.1,
	0.25,
	0.5,
	1,
	2.5,
	5,
	10,
}

// Registry 保存低基数 HTTP 指标。默认关闭，避免兼容应用在未启用观测时承担采集开销。
type Registry struct {
	enabled        atomic.Bool
	inFlight       atomic.Int64
	requests       atomic.Uint64
	errors         atomic.Uint64
	statusClasses  [statusClassCount]atomic.Uint64
	otherStatuses  atomic.Uint64
	durationCount  atomic.Uint64
	durationSum    atomic.Uint64
	durationBucket [bucketCount]atomic.Uint64
}

// Snapshot 是一次无锁一致性近似快照，适合调试、测试和自定义导出器。
type Snapshot struct {
	Enabled         bool
	InFlight        int64
	Requests        uint64
	Errors          uint64
	StatusClasses   [statusClassCount]uint64
	OtherStatuses   uint64
	DurationCount   uint64
	DurationSum     float64
	DurationBuckets [bucketCount]uint64
}

// NewRegistry 创建默认关闭的指标注册表。
func NewRegistry() *Registry {
	return &Registry{}
}

// Enable 开启采集。重复调用是幂等的。
func (r *Registry) Enable() {
	if r != nil {
		r.enabled.Store(true)
	}
}

// Disable 关闭采集。关闭后不再接受新的 Begin，但已开始的请求仍会正确减少 in-flight。
func (r *Registry) Disable() {
	if r != nil {
		r.enabled.Store(false)
	}
}

// Enabled 返回当前是否开启采集。
func (r *Registry) Enabled() bool {
	return r != nil && r.enabled.Load()
}

// Begin 标记一个请求进入处理阶段，并返回是否成功开始采集。
func (r *Registry) Begin() bool {
	if !r.Enabled() {
		return false
	}
	r.inFlight.Add(1)
	return true
}

// End 结束 Begin 创建的观测窗口。
func (r *Registry) End(active bool, status int, duration time.Duration) {
	if r == nil || !active {
		return
	}
	r.inFlight.Add(-1)
	r.observe(status, duration)
}

// Observe 直接记录一次已经完成的请求，不改变 in-flight 数量。
func (r *Registry) Observe(status int, duration time.Duration) {
	if !r.Enabled() {
		return
	}
	r.observe(status, duration)
}

func (r *Registry) observe(status int, duration time.Duration) {
	r.requests.Add(1)
	if status >= http.StatusInternalServerError {
		r.errors.Add(1)
	}
	if status >= http.StatusContinue && status < http.StatusContinue+statusClassCount*100 {
		r.statusClasses[status/100-1].Add(1)
	} else {
		r.otherStatuses.Add(1)
	}
	seconds := duration.Seconds()
	if seconds < 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		seconds = 0
	}
	r.durationCount.Add(1)
	addFloat64(&r.durationSum, seconds)
	for index, bound := range durationBucketBounds {
		if seconds <= bound {
			r.durationBucket[index].Add(1)
		}
	}
	r.durationBucket[len(durationBucketBounds)].Add(1)
}

// Snapshot 返回当前指标快照。
func (r *Registry) Snapshot() Snapshot {
	var snapshot Snapshot
	if r == nil {
		return snapshot
	}
	snapshot.Enabled = r.Enabled()
	snapshot.InFlight = r.inFlight.Load()
	snapshot.Requests = r.requests.Load()
	snapshot.Errors = r.errors.Load()
	for index := range snapshot.StatusClasses {
		snapshot.StatusClasses[index] = r.statusClasses[index].Load()
	}
	snapshot.OtherStatuses = r.otherStatuses.Load()
	snapshot.DurationCount = r.durationCount.Load()
	snapshot.DurationSum = math.Float64frombits(r.durationSum.Load())
	for index := range snapshot.DurationBuckets {
		snapshot.DurationBuckets[index] = r.durationBucket[index].Load()
	}
	return snapshot
}

// Reset 清空全部采集数据，不改变启用状态。
func (r *Registry) Reset() {
	if r == nil {
		return
	}
	r.requests.Store(0)
	r.errors.Store(0)
	for index := range r.statusClasses {
		r.statusClasses[index].Store(0)
	}
	r.otherStatuses.Store(0)
	r.durationCount.Store(0)
	r.durationSum.Store(0)
	for index := range r.durationBucket {
		r.durationBucket[index].Store(0)
	}
}

// Prometheus 返回 Prometheus text exposition 格式的低基数指标。
func (r *Registry) Prometheus() string {
	if !r.Enabled() {
		return ""
	}
	snapshot := r.Snapshot()
	var builder strings.Builder
	builder.Grow(1024)
	writeMetricHeader(&builder, "thinkgo_http_requests_total", "counter", "HTTP 请求总数。")
	fmt.Fprintf(&builder, "thinkgo_http_requests_total %d\n", snapshot.Requests)
	writeMetricHeader(&builder, "thinkgo_http_errors_total", "counter", "HTTP 5xx 响应总数。")
	fmt.Fprintf(&builder, "thinkgo_http_errors_total %d\n", snapshot.Errors)
	writeMetricHeader(&builder, "thinkgo_http_in_flight_requests", "gauge", "当前正在处理的 HTTP 请求数。")
	fmt.Fprintf(&builder, "thinkgo_http_in_flight_requests %d\n", snapshot.InFlight)
	writeMetricHeader(&builder, "thinkgo_http_status_class_requests_total", "counter", "按 HTTP 状态码类别统计的请求数。")
	for index, count := range snapshot.StatusClasses {
		fmt.Fprintf(&builder, "thinkgo_http_status_class_requests_total{class=\"%sxx\"} %d\n", strconv.Itoa(index+1), count)
	}
	fmt.Fprintf(&builder, "thinkgo_http_status_class_requests_total{class=\"other\"} %d\n", snapshot.OtherStatuses)
	writeMetricHeader(&builder, "thinkgo_http_request_duration_seconds", "histogram", "HTTP 请求处理耗时分布。")
	for index, count := range snapshot.DurationBuckets {
		if index == len(durationBucketBounds) {
			fmt.Fprintf(&builder, "thinkgo_http_request_duration_seconds_bucket{le=\"+Inf\"} %d\n", count)
			continue
		}
		fmt.Fprintf(&builder, "thinkgo_http_request_duration_seconds_bucket{le=\"%s\"} %d\n", formatBound(durationBucketBounds[index]), count)
	}
	fmt.Fprintf(&builder, "thinkgo_http_request_duration_seconds_sum %.9f\n", snapshot.DurationSum)
	fmt.Fprintf(&builder, "thinkgo_http_request_duration_seconds_count %d\n", snapshot.DurationCount)
	return builder.String()
}

// ServeHTTP 导出 Prometheus 指标；未启用时返回 404，避免意外暴露观测端点。
func (r *Registry) ServeHTTP(writer http.ResponseWriter, _ *http.Request) {
	if r == nil || !r.Enabled() {
		http.NotFound(writer, nil)
		return
	}
	writer.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	_, _ = writer.Write([]byte(r.Prometheus()))
}

func writeMetricHeader(builder *strings.Builder, name, metricType, help string) {
	fmt.Fprintf(builder, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, metricType)
}

func formatBound(value float64) string {
	return strconv.FormatFloat(value, 'f', -3, 64)
}

func addFloat64(target *atomic.Uint64, value float64) {
	for {
		currentBits := target.Load()
		current := math.Float64frombits(currentBits)
		nextBits := math.Float64bits(current + value)
		if target.CompareAndSwap(currentBits, nextBits) {
			return
		}
	}
}
