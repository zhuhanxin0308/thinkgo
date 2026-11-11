package loadrun

import (
	"sort"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/HdrHistogram/hdrhistogram-go"
)

const histogramSignificantFigures = 3

// Counters 的完成结果互斥，始终满足 planned=started+dropped 和 completed=各结果之和。
type Counters struct {
	Planned             uint64 `json:"planned"`
	Started             uint64 `json:"started"`
	Completed           uint64 `json:"completed"`
	Success             uint64 `json:"success"`
	SuccessWithinWindow uint64 `json:"success_within_window"`
	StatusErrors        uint64 `json:"status_errors"`
	TransportErrors     uint64 `json:"transport_errors"`
	SemanticErrors      uint64 `json:"semantic_errors"`
	Dropped             uint64 `json:"dropped"`
	CacheHits           uint64 `json:"cache_hits"`
	CacheMisses         uint64 `json:"cache_misses"`
}

// HistogramReport 保存完整 HDR 快照；分位数单位毫秒，快照中的数值单位微秒。
type HistogramReport struct {
	Count    uint64                 `json:"count"`
	P50MS    float64                `json:"p50_ms"`
	P95MS    float64                `json:"p95_ms"`
	P99MS    float64                `json:"p99_ms"`
	MaxMS    float64                `json:"max_ms"`
	MeanMS   float64                `json:"mean_ms"`
	Overflow uint64                 `json:"overflow"`
	HDR      *hdrhistogram.Snapshot `json:"hdr_microseconds"`
}

type ScenarioReport struct {
	Totals         Counters          `json:"totals"`
	Statuses       map[string]uint64 `json:"statuses"`
	Latency        HistogramReport   `json:"latency"`
	SuccessLatency HistogramReport   `json:"success_latency"`
	HTTPDuration   HistogramReport   `json:"http_duration"`
	SchedulerLag   HistogramReport   `json:"scheduler_lag"`
}

// SecondReport 按各事件的实际时间归桶；计划数按计划时刻归桶，因此单秒内不要求守恒。
type SecondReport struct {
	Second int64    `json:"second"`
	Counts Counters `json:"counts"`
}

type ErrorSample struct {
	Sequence uint64 `json:"sequence"`
	Scenario string `json:"scenario"`
	Status   int    `json:"status,omitempty"`
	Kind     string `json:"kind"`
	Reason   string `json:"reason"`
}

// Report 显式区分测量窗口内完成吞吐、所发请求排空吞吐和整个运行耗时吞吐。
type Report struct {
	SchemaVersion        int                       `json:"schema_version"`
	ValidatorVersion     int                       `json:"validator_version"`
	Config               Config                    `json:"config"`
	Model                string                    `json:"model"`
	StartedAt            time.Time                 `json:"started_at"`
	FinishedAt           time.Time                 `json:"finished_at"`
	WindowSeconds        float64                   `json:"window_seconds"`
	ElapsedSeconds       float64                   `json:"elapsed_seconds"`
	DrainSeconds         float64                   `json:"drain_seconds"`
	Canceled             bool                      `json:"canceled"`
	Expected             uint64                    `json:"expected"`
	NotScheduled         uint64                    `json:"not_scheduled"`
	Totals               Counters                  `json:"totals"`
	Statuses             map[string]uint64         `json:"statuses"`
	DropReasons          map[string]uint64         `json:"drop_reasons"`
	SuccessRPS           float64                   `json:"success_rps"`
	IssuedSuccessRPS     float64                   `json:"issued_success_rps"`
	ElapsedSuccessRPS    float64                   `json:"elapsed_success_rps"`
	CacheHitRatio        float64                   `json:"cache_hit_ratio"`
	Latency              HistogramReport           `json:"latency"`
	SuccessLatency       HistogramReport           `json:"success_latency"`
	HTTPDuration         HistogramReport           `json:"http_duration"`
	SchedulerLag         HistogramReport           `json:"scheduler_lag"`
	Scenarios            map[string]ScenarioReport `json:"scenarios"`
	Seconds              []SecondReport            `json:"seconds"`
	Errors               []ErrorSample             `json:"error_samples"`
	LatencyDefinition    string                    `json:"latency_definition"`
	ThroughputDefinition string                    `json:"throughput_definition"`
}

type observation struct {
	Sequence  uint64
	Scenario  string
	Scheduled time.Time
	Started   time.Time
	Finished  time.Time
	Status    int
	Outcome   string
	Reason    string
	Cache     string
}

type latencyHistogram struct {
	histogram *hdrhistogram.Histogram
	overflow  uint64
}

func newLatencyHistogram(configuration Config) latencyHistogram {
	highest := (configuration.Duration + configuration.Timeout + tokenExpiryGrace).Microseconds()
	return latencyHistogram{histogram: hdrhistogram.New(1, highest, histogramSignificantFigures)}
}

func (histogram *latencyHistogram) record(duration time.Duration) {
	value := duration.Microseconds()
	if value < 0 {
		value = 0
	} else if duration > 0 && value == 0 {
		value = 1
	}
	if err := histogram.histogram.RecordValue(value); err != nil {
		histogram.overflow++
	}
}

// histogramCount 拒绝异常的负样本数，防止有符号整数转换产生虚假的超大计数。
func histogramCount(total int64) uint64 {
	if total < 0 {
		return 0
	}
	return uint64(total) // #nosec G115 -- 非负的 int64 计数可以无损表示为 uint64
}

func (histogram *latencyHistogram) report() HistogramReport {
	const microsecondsPerMillisecond = float64(time.Millisecond / time.Microsecond)
	value := histogram.histogram
	return HistogramReport{
		Count: histogramCount(value.TotalCount()), P50MS: float64(value.ValueAtPercentile(50)) / microsecondsPerMillisecond,
		P95MS: float64(value.ValueAtPercentile(95)) / microsecondsPerMillisecond,
		P99MS: float64(value.ValueAtPercentile(99)) / microsecondsPerMillisecond,
		MaxMS: float64(value.Max()) / microsecondsPerMillisecond, MeanMS: value.Mean() / microsecondsPerMillisecond,
		Overflow: histogram.overflow, HDR: value.Export(),
	}
}

type scenarioStatistics struct {
	totals         Counters
	statuses       map[string]uint64
	latency        latencyHistogram
	successLatency latencyHistogram
	httpDuration   latencyHistogram
	schedulerLag   latencyHistogram
}

func newScenarioStatistics(configuration Config) *scenarioStatistics {
	return &scenarioStatistics{
		statuses: make(map[string]uint64), latency: newLatencyHistogram(configuration),
		successLatency: newLatencyHistogram(configuration), httpDuration: newLatencyHistogram(configuration), schedulerLag: newLatencyHistogram(configuration),
	}
}

func (statistics *scenarioStatistics) add(event observation, end time.Time) {
	statistics.totals.Planned++
	if event.Outcome == "dropped" {
		statistics.totals.Dropped++
		statistics.schedulerLag.record(event.Finished.Sub(event.Scheduled))
		return
	}
	statistics.totals.Started++
	statistics.latency.record(event.Finished.Sub(event.Scheduled))
	statistics.httpDuration.record(event.Finished.Sub(event.Started))
	statistics.schedulerLag.record(event.Started.Sub(event.Scheduled))
	if event.Status != 0 {
		statistics.statuses[strconv.Itoa(event.Status)]++
	}
	addCompletion(&statistics.totals, event, end)
	if event.Outcome == "success" {
		statistics.successLatency.record(event.Finished.Sub(event.Scheduled))
	}
}

func (statistics *scenarioStatistics) report() ScenarioReport {
	return ScenarioReport{
		Totals: statistics.totals, Statuses: statistics.statuses, Latency: statistics.latency.report(),
		SuccessLatency: statistics.successLatency.report(), HTTPDuration: statistics.httpDuration.report(), SchedulerLag: statistics.schedulerLag.report(),
	}
}

type statistics struct {
	configuration Config
	start         time.Time
	total         *scenarioStatistics
	scenarios     map[string]*scenarioStatistics
	seconds       map[int64]*SecondReport
	errors        []ErrorSample
	dropReasons   map[string]uint64
	windowEnd     atomic.Int64
}

func newStatistics(configuration Config, start time.Time) *statistics {
	result := &statistics{
		configuration: configuration, start: start, total: newScenarioStatistics(configuration),
		scenarios: make(map[string]*scenarioStatistics), seconds: make(map[int64]*SecondReport),
		errors: make([]ErrorSample, 0, configuration.ErrorSampleLimit), dropReasons: make(map[string]uint64),
	}
	result.windowEnd.Store(int64(configuration.Duration))
	return result
}

func (statistics *statistics) second(instant time.Time) *SecondReport {
	index := int64(instant.Sub(statistics.start) / time.Second)
	if index < 0 {
		index = 0
	}
	if statistics.seconds[index] == nil {
		statistics.seconds[index] = &SecondReport{Second: index}
	}
	return statistics.seconds[index]
}

func (statistics *statistics) add(event observation) {
	end := statistics.start.Add(time.Duration(statistics.windowEnd.Load()))
	statistics.total.add(event, end)
	group := statistics.scenarios[event.Scenario]
	if group == nil {
		group = newScenarioStatistics(statistics.configuration)
		statistics.scenarios[event.Scenario] = group
	}
	group.add(event, end)
	statistics.second(event.Scheduled).Counts.Planned++
	if event.Outcome == "dropped" {
		statistics.second(event.Scheduled).Counts.Dropped++
		statistics.dropReasons[event.Reason]++
	} else {
		statistics.second(event.Started).Counts.Started++
		addCompletion(&statistics.second(event.Finished).Counts, event, end)
	}
	if event.Outcome != "success" && len(statistics.errors) < statistics.configuration.ErrorSampleLimit {
		statistics.errors = append(statistics.errors, ErrorSample{Sequence: event.Sequence, Scenario: event.Scenario, Status: event.Status, Kind: event.Outcome, Reason: event.Reason})
	}
}

func addCompletion(counts *Counters, event observation, end time.Time) {
	counts.Completed++
	switch event.Outcome {
	case "success":
		counts.Success++
		if event.Finished.Before(end) {
			counts.SuccessWithinWindow++
		}
	case "status":
		counts.StatusErrors++
	case "transport":
		counts.TransportErrors++
	case "semantic":
		counts.SemanticErrors++
	}
	if event.Cache == "hit" {
		counts.CacheHits++
	} else if event.Cache == "miss" {
		counts.CacheMisses++
	}
}

func (statistics *statistics) finish(finished time.Time, window time.Duration, canceled bool, expected uint64) Report {
	all := statistics.total.report()
	report := Report{
		SchemaVersion: 1, Config: statistics.configuration, Model: "closed", StartedAt: statistics.start.UTC(), FinishedAt: finished.UTC(),
		ValidatorVersion: responseValidatorVersion,
		WindowSeconds:    window.Seconds(), ElapsedSeconds: finished.Sub(statistics.start).Seconds(), Canceled: canceled, Expected: expected,
		Totals: all.Totals, Statuses: all.Statuses, DropReasons: statistics.dropReasons, Latency: all.Latency,
		SuccessLatency: all.SuccessLatency, HTTPDuration: all.HTTPDuration, SchedulerLag: all.SchedulerLag,
		Scenarios: make(map[string]ScenarioReport), Seconds: make([]SecondReport, 0, len(statistics.seconds)), Errors: statistics.errors,
		LatencyDefinition:    "主延迟为计划时刻至正文读取完成；HTTP耗时从实际开始至正文完成；scheduler_lag从计划时刻至HTTP开始或丢弃决策；校验成本影响后续调度但不加入本次HTTP耗时；丢弃请求不伪造主延迟样本",
		ThroughputDefinition: "success_rps=窗口内成功完成数/窗口秒数；issued_success_rps=已发请求排空后的成功数/窗口秒数；elapsed_success_rps=全部成功数/含排空总秒数",
	}
	if statistics.configuration.Rate > 0 {
		report.Model = "open"
	}
	if expected > report.Totals.Planned {
		report.NotScheduled = expected - report.Totals.Planned
	}
	if report.WindowSeconds > 0 {
		report.SuccessRPS = float64(report.Totals.SuccessWithinWindow) / report.WindowSeconds
		report.IssuedSuccessRPS = float64(report.Totals.Success) / report.WindowSeconds
	}
	if report.ElapsedSeconds > 0 {
		report.ElapsedSuccessRPS = float64(report.Totals.Success) / report.ElapsedSeconds
	}
	if report.ElapsedSeconds > report.WindowSeconds {
		report.DrainSeconds = report.ElapsedSeconds - report.WindowSeconds
	}
	if cacheResponses := report.Totals.CacheHits + report.Totals.CacheMisses; cacheResponses > 0 {
		report.CacheHitRatio = float64(report.Totals.CacheHits) / float64(cacheResponses)
	}
	for scenario, group := range statistics.scenarios {
		report.Scenarios[scenario] = group.report()
	}
	for _, second := range statistics.seconds {
		report.Seconds = append(report.Seconds, *second)
	}
	sort.Slice(report.Seconds, func(left, right int) bool { return report.Seconds[left].Second < report.Seconds[right].Second })
	return report
}
