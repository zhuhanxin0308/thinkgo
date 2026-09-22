package debug

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
	"time"

	frameworkContext "github.com/zhuhanxin0308/thinkgo/framework/context"
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
	manager.AddFile("app/index/controller/user.go")

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
	if got := next["files"].([]string)[0]; got != "app/index/controller/user.go" {
		t.Fatalf("外部修改不应污染文件列表，实际为 %v", got)
	}
}

// TestDebugBoundsAndTruncation 验证各类请求调试数据均有固定上限，并能区分具体截断类别。
func TestDebugBoundsAndTruncation(t *testing.T) {
	manager := NewRequestDebug(true)
	for index := 0; index <= MaxLogEntries; index++ {
		manager.AddLog("info", fmt.Sprintf("log-%d", index))
	}
	for index := 0; index <= MaxSQLEntries; index++ {
		manager.AddSql(fmt.Sprintf("select %d", index), time.Millisecond)
	}
	for index := 0; index <= MaxCacheEntries; index++ {
		manager.AddCache("GET", fmt.Sprintf("cache-%d", index))
	}
	for index := 0; index < MaxVarEntries; index++ {
		manager.AddVar(fmt.Sprintf("var-%d", index), index)
	}
	manager.AddVar("var-0", "updated")
	manager.AddVar("var-overflow", true)
	for index := 0; index <= MaxFileEntries; index++ {
		manager.AddFile(fmt.Sprintf("template-%d", index))
	}

	info := manager.GetInfo()
	if got := len(info["logs"].([]map[string]interface{})); got != MaxLogEntries {
		t.Fatalf("日志记录应限制为 %d 条，实际为 %d", MaxLogEntries, got)
	}
	if got := len(info["sqls"].([]map[string]interface{})); got != MaxSQLEntries {
		t.Fatalf("SQL 记录应限制为 %d 条，实际为 %d", MaxSQLEntries, got)
	}
	if got := len(info["cache"].([]map[string]interface{})); got != MaxCacheEntries {
		t.Fatalf("缓存记录应限制为 %d 条，实际为 %d", MaxCacheEntries, got)
	}
	variables := info["vars"].(map[string]interface{})
	if got := len(variables); got != MaxVarEntries {
		t.Fatalf("调试变量应限制为 %d 个键，实际为 %d", MaxVarEntries, got)
	}
	if got := variables["var-0"]; got != "updated" {
		t.Fatalf("达到上限后仍应允许更新已有变量，实际为 %#v", got)
	}
	if got := len(info["files"].([]string)); got != MaxFileEntries {
		t.Fatalf("文件记录应限制为 %d 条，实际为 %d", MaxFileEntries, got)
	}

	for _, kind := range []Kind{KindLog, KindSQL, KindCache, KindVar, KindFile} {
		if !manager.Truncated(kind) {
			t.Fatalf("类别 %q 超限后应标记为已截断", kind)
		}
	}
	if manager.Truncated(Kind("unknown")) {
		t.Fatal("未知调试类别不应报告截断")
	}
}

// TestDebugBoundFilesUseSetAndDefensiveSnapshot 验证文件记录通过集合常数时间去重，快照也不能反向修改内部状态。
func TestDebugBoundFilesUseSetAndDefensiveSnapshot(t *testing.T) {
	manager := NewRequestDebug(true)
	for index := 0; index < MaxFileEntries*4; index++ {
		manager.AddFile("shared-template")
	}
	if len(manager.fileSet) != 1 {
		t.Fatalf("重复文件应只占一个去重集合条目，实际为 %d", len(manager.fileSet))
	}
	if files := manager.GetInfo()["files"].([]string); len(files) != 1 || files[0] != "shared-template" {
		t.Fatalf("重复文件记录去重错误: %#v", files)
	}

	snapshot := manager.GetInfo()
	snapshot["files"].([]string)[0] = "changed-template"
	snapshot["truncated"].(map[string]bool)[string(KindFile)] = true
	if files := manager.GetInfo()["files"].([]string); files[0] != "shared-template" {
		t.Fatalf("调用方修改文件快照不应污染内部集合，实际为 %#v", files)
	}
	if manager.Truncated(KindFile) {
		t.Fatal("调用方修改截断快照不应污染内部状态")
	}

	manager.Clear()
	if len(manager.fileSet) != 0 || manager.Truncated(KindFile) {
		t.Fatal("Clear 应同时重置文件去重集合和截断状态")
	}
}

// TestDebugBoundsRemainSafeUnderConcurrency 验证并发采集不会突破任一边界或破坏去重集合。
func TestDebugBoundsRemainSafeUnderConcurrency(t *testing.T) {
	manager := NewRequestDebug(true)
	const workers = 16
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		worker := worker
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := 0; index < MaxLogEntries; index++ {
				key := fmt.Sprintf("%d-%d", worker, index)
				manager.AddLog("info", key)
				manager.AddSql(key, time.Microsecond)
				manager.AddCache("GET", key)
				manager.AddVar(key, index)
				manager.AddFile(key)
			}
		}()
	}
	wait.Wait()

	info := manager.GetInfo()
	if len(info["logs"].([]map[string]interface{})) > MaxLogEntries ||
		len(info["sqls"].([]map[string]interface{})) > MaxSQLEntries ||
		len(info["cache"].([]map[string]interface{})) > MaxCacheEntries ||
		len(info["vars"].(map[string]interface{})) > MaxVarEntries ||
		len(info["files"].([]string)) > MaxFileEntries {
		t.Fatalf("并发采集突破边界: %#v", info)
	}
}

// TestDebugFromRequestUsesCanonicalKey 验证请求 collector 只通过 debug 包的统一键读取。
func TestDebugFromRequestUsesCanonicalKey(t *testing.T) {
	request := frameworkContext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com", nil))
	if collector := FromRequest(request); collector != nil {
		t.Fatalf("未挂载 collector 时应返回 nil，实际为 %#v", collector)
	}
	collector := NewRequestDebug(true)
	request.Set(RequestKey, collector)
	if got := FromRequest(request); got != collector {
		t.Fatalf("请求 collector 读取错误，期望 %#v，实际为 %#v", collector, got)
	}
	request.Set(RequestKey, "invalid")
	if got := FromRequest(request); got != nil {
		t.Fatalf("错误类型不得被当作 collector，实际为 %#v", got)
	}
	if got := FromRequest(nil); got != nil {
		t.Fatalf("空请求应返回 nil，实际为 %#v", got)
	}
}
