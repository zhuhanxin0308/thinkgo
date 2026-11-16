package debug

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

	frameworkContext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

// TestDebugVariablesAreIsolated 验证写入和读取得到的调试值均与收集器状态隔离。
func TestDebugVariablesAreIsolated(t *testing.T) {
	manager := NewRequestDebug(true)
	type record struct{ Roles []string }
	entry := &record{Roles: []string{"reader"}}
	original := map[string][]*record{"team": {entry, entry}}
	manager.AddVar("state", original)
	entry.Roles[0] = "source mutation"
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			values := manager.GetInfo()["vars"].(map[string]interface{})["state"].(map[string][]*record)["team"]
			if values[0] != values[1] || values[0].Roles[0] != "reader" {
				t.Error("调试快照应保存原始值和同一快照内的别名")
				return
			}
			values[0].Roles[0] = "snapshot mutation"
		}()
	}
	workers.Wait()
	values := manager.GetInfo()["vars"].(map[string]interface{})["state"].(map[string][]*record)["team"]
	if values[0].Roles[0] != "reader" {
		t.Fatal("修改嵌套快照不应污染后续调试数据")
	}
}

// TestDebugVariablesMarkUnsupportedValues 验证不能复制的值用固定提示显示。
func TestDebugVariablesMarkUnsupportedValues(t *testing.T) {
	manager := NewRequestDebug(true)
	cycle := map[string]interface{}{}
	cycle["self"] = cycle
	manager.AddVar("cycle", cycle)
	manager.AddVar("lock", &sync.Mutex{})
	manager.AddVar("ready", true)
	vars := manager.GetInfo()["vars"].(map[string]interface{})
	for _, key := range []string{"cycle", "lock"} {
		if got, ok := vars[key].(string); !ok || got != "[无法安全复制的调试值]" {
			t.Errorf("不可复制的变量 %q 应显示固定提示，实际类型为 %T", key, vars[key])
		}
	}
	if got := vars["ready"]; got != true {
		t.Fatalf("合法调试变量应正常读取: %v", got)
	}
}

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

func TestDebugEntrySnapshotsPreserveFieldsAndTime(t *testing.T) {
	manager := NewRequestDebug(true)
	now := time.Date(2026, 4, 15, 12, 3, 4, int(567*time.Millisecond), time.UTC)
	manager.now = func() time.Time { return now }
	manager.SetLocation(time.FixedZone("应用时区", int((2 * time.Hour).Seconds())))
	manager.AddLog("info", "first")
	manager.SetLocation(time.UTC)
	manager.AddLog("warn", "second")
	manager.AddSql("select 1", 2500*time.Millisecond)
	manager.AddCache("get", "user:1")

	snapshot := manager.GetInfo()
	expected := map[string][]map[string]interface{}{
		"logs":  {{"level": "info", "msg": "first", "time": "14:03:04.567"}, {"level": "warn", "msg": "second", "time": "12:03:04.567"}},
		"sqls":  {{"sql": "select 1", "duration": 2.5, "time": "12:03:04.567"}},
		"cache": {{"op": "get", "key": "user:1", "time": "12:03:04.567"}},
	}
	manager.Clear()
	manager.AddLog("error", "third")
	for category, entries := range expected {
		if !reflect.DeepEqual(snapshot[category], entries) {
			t.Errorf("%s 快照字段或类型发生改变: %#v", category, snapshot[category])
		}
	}
}

func TestDebugEntryCaptureAllocationBudget(t *testing.T) {
	manager := NewRequestDebug(true)
	const entriesPerKind = 40
	allocations := testing.AllocsPerRun(20, func() {
		manager.Clear()
		for range entriesPerKind {
			manager.AddLog("info", "entry")
			manager.AddSql("select 1", time.Millisecond)
			manager.AddCache("get", "user:1")
		}
	})
	// 在复用切片后，允许每条记录为展示时间及其附加开销分配，避免恢复每条记录独立的 map。
	const totalEntries = entriesPerKind * 3
	t.Logf("采集 %d 条记录分配了 %.0f 次", totalEntries, allocations)
	if allocations > 2*totalEntries {
		t.Fatalf("采集 %d 条记录分配了 %.0f 次，超过预算 %d", totalEntries, allocations, 2*totalEntries)
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
