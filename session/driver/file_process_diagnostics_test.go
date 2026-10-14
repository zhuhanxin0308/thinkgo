package driver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// fileProcessChild 的输出只能在 Wait 完成后读取，成功进程的轨迹也必须保留。
type fileProcessChild struct {
	index   int
	command *exec.Cmd
	stdout  bytes.Buffer
	stderr  bytes.Buffer
	waitErr error
}

// fileProcessWaitChildren 不在首个失败处返回，确保全部进程和输出复制协程已回收。
func fileProcessWaitChildren(children []*fileProcessChild) (failed bool) {
	for _, child := range children {
		child.waitErr = child.command.Wait()
		failed = failed || child.waitErr != nil
	}
	return failed
}

// fileProcessChildDiagnostics 在所有 Wait 完成后生成诊断，不过滤成功写者。
func fileProcessChildDiagnostics(children []*fileProcessChild) string {
	var output strings.Builder
	for _, child := range children {
		pid := 0
		if child.command.Process != nil {
			pid = child.command.Process.Pid
		}
		fmt.Fprintf(&output, "子进程 %d pid=%d success=%t wait_error=%v\nstdout:\n%s\nstderr:\n%s\n",
			child.index, pid, child.waitErr == nil, child.waitErr, child.stdout.String(), child.stderr.String())
	}
	return output.String()
}

// 时间点围绕同步 Update 回调采集；不是内核锁的精确获取／释放时间。
type fileProcessUpdateTiming struct {
	started          time.Time
	callbackStarted  time.Time
	callbackFinished time.Time
	finished         time.Time
}

// fileProcessMeasureUpdate 只观测回调，不重试、改变结果或吞掉错误。
func fileProcessMeasureUpdate(
	run func(func(string, bool) (string, bool, error)) error,
	update func(string, bool) (string, bool, error),
	now func() time.Time,
) (timing fileProcessUpdateTiming, err error) {
	timing.started = now()
	err = run(func(current string, found bool) (string, bool, error) {
		timing.callbackStarted = now()
		defer func() { timing.callbackFinished = now() }()
		return update(current, found)
	})
	timing.finished = now()
	return timing, err
}

func fileProcessTimingDiagnostics(timings []fileProcessUpdateTiming) string {
	var output strings.Builder
	for index, timing := range timings {
		fmt.Fprintf(&output, "update=%d start=%s end=%s total=%s",
			index+1, timing.started.UTC().Format(time.RFC3339Nano),
			timing.finished.UTC().Format(time.RFC3339Nano), timing.finished.Sub(timing.started))
		if timing.callbackStarted.IsZero() {
			output.WriteString(" callback=not_entered\n")
			continue
		}
		fmt.Fprintf(&output, " callback=entered before_callback=%s callback_time=%s after_callback=%s\n",
			timing.callbackStarted.Sub(timing.started), timing.callbackFinished.Sub(timing.callbackStarted),
			timing.finished.Sub(timing.callbackFinished))
	}
	return output.String()
}

// 使用离散时钟验证区段归属，不依赖真实睡眠或机器调度。
func TestFileProcessMeasureUpdate(t *testing.T) {
	callbackErr := errors.New("callback failure")
	commitErr := errors.New("commit failure")
	for _, test := range []struct {
		name        string
		callbackErr error
		commitErr   error
	}{
		{name: "success"},
		{name: "callback_error", callbackErr: callbackErr},
		{name: "commit_error", commitErr: commitErr},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
			offsets := []time.Duration{0, 2 * time.Millisecond, 3 * time.Millisecond, 7 * time.Millisecond}
			clockCalls, updateCalls, runCalls := 0, 0, 0
			now := func() time.Time {
				if clockCalls == len(offsets) {
					t.Fatal("时钟调用次数超出预期")
				}
				value := base.Add(offsets[clockCalls])
				clockCalls++
				return value
			}
			timing, err := fileProcessMeasureUpdate(func(update func(string, bool) (string, bool, error)) error {
				runCalls++
				next, remove, err := update("original", true)
				if next != "updated" || !remove || err != test.callbackErr {
					t.Fatalf("回调结果被改变: %q %t %v", next, remove, err)
				}
				if err != nil {
					return err
				}
				return test.commitErr
			}, func(current string, found bool) (string, bool, error) {
				updateCalls++
				if current != "original" || !found {
					t.Fatalf("回调输入被改变: %q %t", current, found)
				}
				return "updated", true, test.callbackErr
			}, now)
			wantErr := test.callbackErr
			if wantErr == nil {
				wantErr = test.commitErr
			}
			if err != wantErr || clockCalls != 4 || updateCalls != 1 || runCalls != 1 {
				t.Fatalf("执行次数或错误身份改变: err=%v clocks=%d updates=%d runs=%d", err, clockCalls, updateCalls, runCalls)
			}
			want := "update=1 start=2026-10-14T00:00:00Z end=2026-10-14T00:00:00.007Z total=7ms callback=entered before_callback=2ms callback_time=1ms after_callback=4ms\n"
			if got := fileProcessTimingDiagnostics([]fileProcessUpdateTiming{timing}); got != want {
				t.Fatalf("更新时序错误:\n%s", got)
			}
		})
	}
}

func TestFileProcessMeasureUpdateWithoutCallback(t *testing.T) {
	base := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	clockCalls := 0
	timing, err := fileProcessMeasureUpdate(func(func(string, bool) (string, bool, error)) error {
		return context.DeadlineExceeded
	}, func(string, bool) (string, bool, error) {
		t.Fatal("等待失败时不应执行回调")
		return "", false, nil
	}, func() time.Time {
		value := base.Add(time.Duration(clockCalls) * 15 * time.Second)
		clockCalls++
		return value
	})
	if err != context.DeadlineExceeded || clockCalls != 2 || !timing.callbackStarted.IsZero() || !timing.callbackFinished.IsZero() {
		t.Fatalf("未进入回调的错误或时间点错误: %v %#v", err, timing)
	}
	want := "update=1 start=2026-10-14T00:00:00Z end=2026-10-14T00:00:15Z total=15s callback=not_entered\n"
	if got := fileProcessTimingDiagnostics([]fileProcessUpdateTiming{timing}); got != want {
		t.Fatalf("不得把未进入回调报告为零持锁时间: %s", got)
	}
	if got := fileProcessTimingDiagnostics(nil); got != "" {
		t.Fatalf("空更新记录应为空: %q", got)
	}
}

const fileProcessDiagnosticChildEnv = "THINKGO_FILE_PROCESS_DIAGNOSTIC_CHILD"

// 本辅助进程只用于诊断器回归，不访问 Session，也不模拟生产锁超时。
func TestFileProcessDiagnosticChild(t *testing.T) {
	mode := os.Getenv(fileProcessDiagnosticChildEnv)
	if mode == "" {
		return
	}
	t.Logf("diagnostic-child-stdout-%s", mode)
	fmt.Fprintf(os.Stderr, "diagnostic-child-stderr-%s\n", mode)
	if mode != "success" {
		t.Fatal("intentional diagnostic child failure")
	}
}

func TestFileProcessWaitChildrenRetainsEveryTranscript(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var children []*fileProcessChild
	for index, mode := range []string{"failure", "success"} {
		child := &fileProcessChild{index: index}
		child.command = exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFileProcessDiagnosticChild$", "-test.v")
		child.command.Env = append(os.Environ(), fileProcessDiagnosticChildEnv+"="+mode)
		child.command.Stdout, child.command.Stderr = &child.stdout, &child.stderr
		if err := child.command.Start(); err != nil {
			cancel()
			fileProcessWaitChildren(children)
			t.Fatalf("启动诊断辅助进程失败: %v", err)
		}
		children = append(children, child)
	}
	if !fileProcessWaitChildren(children) {
		t.Fatal("失败进程不应被忽略")
	}
	var exitErr *exec.ExitError
	if !errors.As(children[0].waitErr, &exitErr) || exitErr.ExitCode() != 1 || children[1].waitErr != nil || ctx.Err() != nil {
		t.Fatalf("实际退出状态错误: first=%v second=%v context=%v", children[0].waitErr, children[1].waitErr, ctx.Err())
	}
	for _, child := range children {
		if child.command.ProcessState == nil || !child.command.ProcessState.Exited() {
			t.Fatal("必须等待全部进程后才读取输出")
		}
	}
	got := fileProcessChildDiagnostics(children)
	for _, want := range []string{"子进程 0 pid=", "子进程 1 pid=", "success=false", "success=true",
		"diagnostic-child-stdout-failure", "diagnostic-child-stderr-failure",
		"diagnostic-child-stdout-success", "diagnostic-child-stderr-success"} {
		if !strings.Contains(got, want) {
			t.Errorf("完整进程诊断缺少 %q:\n%s", want, got)
		}
	}
	if fileProcessWaitChildren(nil) || fileProcessChildDiagnostics(nil) != "" {
		t.Fatal("空进程集合不应报告失败或输出")
	}
}
