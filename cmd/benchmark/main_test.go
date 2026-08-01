package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	hdrhistogram "github.com/HdrHistogram/hdrhistogram-go"
	"thinkgo/framework"
)

// TestBenchmarkDBParamsTLSPolicy 验证本机基准保留兼容行为，远程目标默认启用证书校验。
func TestBenchmarkDBParamsTLSPolicy(t *testing.T) {
	testCases := []struct {
		name     string
		host     string
		explicit string
		want     string
	}{
		{name: "loopback", host: "127.0.0.1", want: localBenchmarkTLSMode},
		{name: "localhost", host: "localhost", want: localBenchmarkTLSMode},
		{name: "ipv6 loopback", host: "[::1]", want: localBenchmarkTLSMode},
		{name: "remote", host: "db.example.com", want: verifiedBenchmarkTLSMode},
		{name: "explicit override", host: "db.example.com", explicit: localBenchmarkTLSMode, want: localBenchmarkTLSMode},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("DB_HOST", testCase.host)
			t.Setenv("DB_TLS_MODE", testCase.explicit)
			basePath := t.TempDir()
			writeBenchmarkTestConfig(t, basePath)
			app, err := framework.BuildConsoleApp(basePath)
			if err != nil {
				t.Fatalf("构建基准测试应用失败: %v", err)
			}
			t.Cleanup(func() { _ = app.Close() })
			if got := benchmarkDBParams(app)["tls"]; got != testCase.want {
				t.Fatalf("TLS 模式错误: got %q want %q", got, testCase.want)
			}
		})
	}

	// writeBenchmarkTestConfig 为基准参数测试准备不依赖外部数据库的控制台配置。
}

func writeBenchmarkTestConfig(t *testing.T, basePath string) {
	t.Helper()
	configPath := filepath.Join(basePath, "config")
	if err := os.MkdirAll(configPath, 0o755); err != nil {
		t.Fatalf("创建基准测试配置目录失败: %v", err)
	}
	configs := map[string]string{
		"app.json":     `{"app_env":"test","server":{"host":"127.0.0.1","port":8080},"compression":{"enable":false}}`,
		"log.json":     `{"default":"file","channels":{"file":{"type":"file","path":"runtime/log"}}}`,
		"cache.json":   `{"default":"file","stores":{"file":{"type":"file","path":"runtime/cache"}}}`,
		"view.json":    `{"view_path":"app/view","view_suffix":"html","cache":false}`,
		"cookie.json":  `{}`,
		"session.json": `{"type":"memory","name":"TESTSESSID","expire":600}`,
	}
	for name, content := range configs {
		if err := os.WriteFile(filepath.Join(configPath, name), []byte(content), 0o644); err != nil {
			t.Fatalf("写入基准测试配置 %q 失败: %v", name, err)
		}
	}
}

// TestIsLoopbackBenchmarkHostRejectsAmbiguousTargets 验证只有明确回环地址才能进入本地证书兼容策略。
func TestIsLoopbackBenchmarkHostRejectsAmbiguousTargets(t *testing.T) {
	testCases := map[string]bool{
		"127.0.0.1":      true,
		"127.0.0.2":      true,
		"::1":            true,
		"[::1]":          true,
		"localhost":      true,
		"db.example.com": false,
		"192.168.1.10":   false,
		"127.0.0.1:3306": false,
	}
	for host, want := range testCases {
		if got := isLoopbackBenchmarkHost(host); got != want {
			t.Fatalf("回环地址判断错误: host=%q got=%t want=%t", host, got, want)
		}
	}
}

// TestBenchmarkTablePrefixIsolatedAndValidated 验证基准表名必须使用受限且可复用的隔离前缀。
func TestBenchmarkTablePrefixIsolatedAndValidated(t *testing.T) {
	previous := benchmarkTablePrefix
	t.Cleanup(func() { benchmarkTablePrefix = previous })
	if err := setBenchmarkTablePrefix("bench_run_123_"); err != nil {
		t.Fatalf("合法基准表前缀不应失败: %v", err)
	}
	if got := benchmarkTable("products"); got != "bench_run_123_products" {
		t.Fatalf("基准表名未应用隔离前缀: %q", got)
	}
	for _, prefix := range []string{"", "bench", "bench-run_", "bench_;DROP_"} {
		if err := setBenchmarkTablePrefix(prefix); err == nil {
			t.Fatalf("非法基准表前缀应失败: %q", prefix)
		}
	}
}

// TestLatencyHistogramAcceptsZeroAndSubPrecision 验证零延迟和低于精度下限的非负值不会被误判为越界。
func TestLatencyHistogramAcceptsZeroAndSubPrecision(t *testing.T) {
	histogram := newLatencyHistogram()
	if err := recordLatency(histogram, 0); err != nil {
		t.Fatalf("零延迟应被记录: %v", err)
	}
	if err := recordLatency(histogram, time.Duration(lowestDiscernibleValue/2)); err != nil {
		t.Fatalf("低于精度下限的延迟应被记录: %v", err)
	}
	if histogram.TotalCount() != 2 {
		t.Fatalf("延迟样本数错误: got=%d want=2", histogram.TotalCount())
	}
}

// TestLatencyHistogramRejectsNegativeAndOutOfRange 验证无效和超出边界的样本会返回可观察错误。
func TestLatencyHistogramRejectsNegativeAndOutOfRange(t *testing.T) {
	histogram := newLatencyHistogram()
	if err := recordLatency(histogram, -time.Nanosecond); err == nil {
		t.Fatal("负延迟必须返回错误")
	}
	if err := recordLatency(histogram, time.Duration(highestTrackableValue)+time.Nanosecond); err == nil {
		t.Fatal("超出最高可跟踪值的延迟必须返回错误")
	}
}

// TestMergeLatencyHistogramReportsDroppedSamples 验证 HDR 合并丢样本时不会静默吞掉错误。
func TestMergeLatencyHistogramReportsDroppedSamples(t *testing.T) {
	destination := hdrhistogram.New(lowestDiscernibleValue, int64(time.Millisecond), significantValueDigits)
	source := hdrhistogram.New(lowestDiscernibleValue, int64(time.Second), significantValueDigits)
	if err := recordLatency(source, 500*time.Millisecond); err != nil {
		t.Fatalf("构造源样本失败: %v", err)
	}
	err := mergeLatencyHistogram(destination, source)
	if err == nil || !strings.Contains(err.Error(), "dropped") {
		t.Fatalf("合并丢样本必须返回可观察错误: %v", err)
	}
}

// TestLatencyHistogramPercentilesUseMilliseconds 验证百分位和最大值保持毫秒输出单位。
func TestLatencyHistogramPercentilesUseMilliseconds(t *testing.T) {
	histogram := newLatencyHistogram()
	for _, latency := range []time.Duration{time.Millisecond, 2 * time.Millisecond, 3 * time.Millisecond, 4 * time.Millisecond} {
		if err := recordLatency(histogram, latency); err != nil {
			t.Fatalf("记录延迟失败: %v", err)
		}
	}
	if value := histogramLatencyAt(histogram, 0.50); value < 1 || value > 4 {
		t.Fatalf("P50 毫秒值不在预期范围: %f", value)
	}
	if value := histogramMaxMilliseconds(histogram); value < 1 || value > 4.1 {
		t.Fatalf("最大延迟毫秒值不在预期范围: %f", value)
	}
}

// TestRunLoadValidationPreservesCLIContract 验证现有 load 参数校验仍然保持原错误边界。
func TestRunLoadValidationPreservesCLIContract(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		duration    time.Duration
		concurrency int
		scenario    string
	}{
		{name: "duration", duration: 0, concurrency: 1, scenario: "health"},
		{name: "concurrency", duration: time.Second, concurrency: 0, scenario: "health"},
		{name: "scenario", duration: time.Second, concurrency: 1, scenario: "unknown"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if err := runLoad("http://127.0.0.1:1", testCase.duration, testCase.concurrency, testCase.scenario); err == nil {
				t.Fatal("非法参数必须返回错误")
			}
		})
	}
	if err := mergeLatencyHistogram(nil, newLatencyHistogram()); !errors.Is(err, errLatencyHistogramUnavailable) {
		t.Fatalf("空直方图依赖应返回稳定错误: %v", err)
	}
}

// TestRunLoadMergesWorkerHistograms 验证多个 worker 的固定直方图可以合并并完成百分位汇总。
func TestRunLoadMergesWorkerHistograms(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	if err := runLoad(server.URL, 150*time.Millisecond, 2, "health"); err != nil {
		t.Fatalf("本地基准负载汇总失败: %v", err)
	}
}

// TestEnvBoolUsesFallbackOnEmptyOrInvalid 验证可选布尔配置不会因空值或非法值改变默认行为。
func TestEnvBoolUsesFallbackOnEmptyOrInvalid(t *testing.T) {
	if got := envBool(nil, "", true); !got {
		t.Fatal("空配置应保留 true 默认值")
	}
	if got := envBool(nil, "", false); got {
		t.Fatal("空配置应保留 false 默认值")
	}
	if got := parseBoolOrFallback("invalid", true); !got {
		t.Fatal("非法配置应回退到 true 默认值")
	}
	if got := parseBoolOrFallback("false", true); got {
		t.Fatal("显式 false 配置未生效")
	}
}
