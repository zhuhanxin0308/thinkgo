package deploy

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3"
)

// TestAuditorReleasesExecutionSlotBeforeReturn 验证检查完成后执行槽严格先于结果可见归还，
// 防止剩余槽位紧张时下一项连续检查误判执行槽已耗尽。
func TestAuditorReleasesExecutionSlotBeforeReturn(t *testing.T) {
	auditor, err := NewAuditor()
	if err != nil {
		t.Fatal(err)
	}
	// 预先占用其余所有执行槽，仅保留 1 个空闲槽模拟存在不协作检查时的边界容量。
	for index := 0; index < cap(auditor.executionSlots)-1; index++ {
		auditor.executionSlots <- struct{}{}
	}
	defer func() {
		for index := 0; index < cap(auditor.executionSlots)-1; index++ {
			<-auditor.executionSlots
		}
	}()

	passCheck := checkFunc{name: "slot.pass", run: func(context.Context, *framework.App) Result { return pass("ok") }}
	panicCheck := checkFunc{name: "slot.panic", run: func(context.Context, *framework.App) Result { panic("boom") }}
	for iteration := 0; iteration < 50000; iteration++ {
		result := auditor.runCheckWithDeadline(context.Background(), nil, passCheck)
		if result.Level != LevelPass {
			t.Fatalf("第 %d 次顺序检查触发假性槽位耗尽或异常失败: %#v", iteration, result)
		}
		if occupied := len(auditor.executionSlots); occupied != cap(auditor.executionSlots)-1 {
			t.Fatalf("第 %d 次检查返回时执行槽尚未归还: occupied=%d", iteration, occupied)
		}
	}
	failed := auditor.runCheckWithDeadline(context.Background(), nil, panicCheck)
	if failed.Level != LevelFail || !strings.Contains(failed.Message, "panic") {
		t.Fatalf("panic 检查应返回失败结果: %#v", failed)
	}
	if occupied := len(auditor.executionSlots); occupied != cap(auditor.executionSlots)-1 {
		t.Fatalf("panic 检查返回时执行槽未归还: occupied=%d", occupied)
	}
}

// TestAuditorGoexitReleasesExecutionSlot 验证检查协程调用 runtime.Goexit 时不会泄漏执行槽。
func TestAuditorGoexitReleasesExecutionSlot(t *testing.T) {
	auditor, err := NewAuditor()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	goexitCheck := checkFunc{name: "slot.goexit", run: func(context.Context, *framework.App) Result {
		runtime.Goexit()
		return pass("unreachable")
	}}
	result := auditor.runCheckWithDeadline(ctx, nil, goexitCheck)
	if result.Level != LevelFail || !strings.Contains(result.Message, "超时") {
		t.Fatalf("Goexit 检查应以超时失败收尾: %#v", result)
	}
	if occupied := len(auditor.executionSlots); occupied != 0 {
		t.Fatalf("Goexit 后不应泄漏执行槽: occupied=%d", occupied)
	}
}
