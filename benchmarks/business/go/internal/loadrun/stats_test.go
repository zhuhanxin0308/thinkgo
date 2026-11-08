package loadrun

import (
	"testing"
	"time"

	"github.com/HdrHistogram/hdrhistogram-go"
)

func TestHistogramAndWindowAccounting(t *testing.T) {
	configuration := testConfig()
	configuration.Duration = time.Second
	start := time.Unix(1700000000, 0)
	statistics := newStatistics(configuration, start)
	for index := 1; index <= 100; index++ {
		statistics.add(observation{
			Sequence: uint64(index), Scenario: "detail", Scheduled: start,
			Started: start.Add(time.Millisecond), Finished: start.Add(time.Duration(index+1) * time.Millisecond),
			Outcome: "success", Status: 200, Cache: "hit",
		})
	}
	statistics.add(observation{
		Sequence: 101, Scenario: "detail", Scheduled: start.Add(900 * time.Millisecond),
		Started: start.Add(950 * time.Millisecond), Finished: start.Add(1100 * time.Millisecond),
		Outcome: "success", Status: 200, Cache: "miss",
	})
	report := statistics.finish(start.Add(1200*time.Millisecond), time.Second, false, 101)
	assertConservation(t, report)
	if report.SuccessRPS != 100 || report.IssuedSuccessRPS != 101 || report.Totals.CacheHits != 100 || report.Totals.CacheMisses != 1 || report.Totals.SuccessWithinWindow != 100 {
		t.Fatalf("窗口或命中率统计错误: %#v", report.Totals)
	}
	if report.Latency.Count != 101 || report.Latency.P50MS < 50 || report.Latency.P50MS > 53 || report.Latency.MaxMS < 199 || report.Latency.MaxMS > 202 {
		t.Fatalf("分位数错误: %#v", report.Latency)
	}
	restored := hdrhistogram.Import(report.Latency.HDR)
	if restored.TotalCount() != int64(report.Latency.Count) {
		t.Fatal("直方图快照不能完整恢复")
	}
	if report.Scenarios["detail"].Totals.Success != 101 || report.Scenarios["detail"].Latency.Count != 101 {
		t.Fatal("场景维度与总计不一致")
	}
}

func TestEmptyCanceledReportAndDropReasons(t *testing.T) {
	configuration := testConfig()
	start := time.Now()
	statistics := newStatistics(configuration, start)
	statistics.add(observation{Scenario: "catalog", Scheduled: start, Finished: start.Add(time.Millisecond), Outcome: "dropped", Reason: "inflight_limit"})
	report := statistics.finish(start.Add(time.Millisecond), time.Millisecond, true, 10)
	assertConservation(t, report)
	if report.NotScheduled != 9 || report.Totals.Dropped != 1 || report.DropReasons["inflight_limit"] != 1 || report.Latency.Count != 0 || report.SuccessRPS != 0 {
		t.Fatalf("空结果或丢弃原因不准确: %#v", report)
	}
}

func TestCancellationCutoffExcludesDrainCompletions(t *testing.T) {
	configuration := testConfig()
	configuration.Duration = 2 * time.Second
	start := time.Now()
	statistics := newStatistics(configuration, start)
	statistics.add(observation{Scenario: "catalog", Scheduled: start, Started: start, Finished: start.Add(500 * time.Millisecond), Outcome: "success", Status: 200})
	statistics.windowEnd.Store(int64(time.Second))
	statistics.add(observation{Scenario: "catalog", Scheduled: start.Add(900 * time.Millisecond), Started: start.Add(900 * time.Millisecond), Finished: start.Add(1500 * time.Millisecond), Outcome: "success", Status: 200})
	report := statistics.finish(start.Add(1500*time.Millisecond), time.Second, true, 2)
	assertConservation(t, report)
	if report.Totals.Success != 2 || report.Totals.SuccessWithinWindow != 1 || report.SuccessRPS != 1 || report.IssuedSuccessRPS != 2 {
		t.Fatalf("取消后完成的请求错误计入发送窗口吞吐: %#v", report.Totals)
	}
}
