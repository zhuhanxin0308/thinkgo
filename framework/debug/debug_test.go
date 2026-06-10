package debug

import (
	"runtime"
	"testing"
	"time"
)

// TestMemStatsSamplerCachesWithinInterval 验证内存采样器会在采样窗口内复用最近一次结果，避免每次请求都读取运行时内存统计。
func TestMemStatsSamplerCachesWithinInterval(t *testing.T) {
	readCount := 0
	now := time.Date(2026, 4, 15, 12, 0, 0, 0, time.Local)
	sampler := newMemStatsSampler(
		5*time.Second,
		func(stats *runtime.MemStats) {
			readCount++
			stats.Alloc = uint64(1024 * readCount)
		},
	)
	sampler.now = func() time.Time { return now }

	first := sampler.CurrentAlloc()
	second := sampler.CurrentAlloc()

	if readCount != 1 {
		t.Fatalf("采样窗口内应只读取一次运行时内存统计，实际读取 %d 次", readCount)
	}
	if first != second {
		t.Fatalf("采样窗口内应复用最近结果，第一次=%d，第二次=%d", first, second)
	}
}

// TestMemStatsSamplerRefreshesAfterInterval 验证超过采样窗口后会重新读取一次内存统计。
func TestMemStatsSamplerRefreshesAfterInterval(t *testing.T) {
	readCount := 0
	now := time.Date(2026, 4, 15, 12, 0, 0, 0, time.Local)
	sampler := newMemStatsSampler(
		2*time.Second,
		func(stats *runtime.MemStats) {
			readCount++
			stats.Alloc = uint64(2048 * readCount)
		},
	)
	sampler.now = func() time.Time { return now }

	first := sampler.CurrentAlloc()
	now = now.Add(3 * time.Second)
	second := sampler.CurrentAlloc()

	if readCount != 2 {
		t.Fatalf("超过采样窗口后应重新读取内存统计，实际读取 %d 次", readCount)
	}
	if first == second {
		t.Fatalf("重新采样后内存值应更新，第一次=%d，第二次=%d", first, second)
	}
}
