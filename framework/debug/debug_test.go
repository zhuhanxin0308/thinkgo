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

// TestDebugGetInfoReturnsSnapshot 验证 GetInfo 返回快照，调用方不能篡改内部调试数据。
func TestDebugGetInfoReturnsSnapshot(t *testing.T) {
	manager := NewRequestDebug(true)
	manager.AddLog("info", "first")
	manager.AddSql("select 1", time.Millisecond)
	manager.AddCache("get", "user:1")
	manager.AddVar("user", "alice")
	manager.AddFile("app/controller/user.go")

	info := manager.GetInfo()
	info["vars"].(map[string]interface{})["user"] = "mallory"
	info["logs"].([]map[string]interface{})[0]["msg"] = "changed"
	info["sqls"].([]map[string]interface{})[0]["sql"] = "drop table users"
	info["cache"].([]map[string]interface{})[0]["key"] = "changed"
	info["files"].([]string)[0] = "changed.go"

	next := manager.GetInfo()
	if got := next["vars"].(map[string]interface{})["user"]; got != "alice" {
		t.Fatalf("外部修改不应污染调试变量，实际为 %v", got)
	}
	if got := next["logs"].([]map[string]interface{})[0]["msg"]; got != "first" {
		t.Fatalf("外部修改不应污染日志条目，实际为 %v", got)
	}
	if got := next["sqls"].([]map[string]interface{})[0]["sql"]; got != "select 1" {
		t.Fatalf("外部修改不应污染 SQL 条目，实际为 %v", got)
	}
	if got := next["cache"].([]map[string]interface{})[0]["key"]; got != "user:1" {
		t.Fatalf("外部修改不应污染缓存条目，实际为 %v", got)
	}
	if got := next["files"].([]string)[0]; got != "app/controller/user.go" {
		t.Fatalf("外部修改不应污染文件列表，实际为 %v", got)
	}
}
