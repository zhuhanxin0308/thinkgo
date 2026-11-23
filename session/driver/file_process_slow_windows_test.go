//go:build windows

package driver

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	fileProcessSlowWorkerMode = "slow-atomic-counter"
	fileProcessSlowStartLine  = "start\n"
	fileProcessSlowHold       = 125 * time.Millisecond
	fileProcessReapReserve    = 5 * time.Second
)

// 八个独立写者从同一屏障出发，各执行 25 次带慢回调的真实 Update。
// 生产锁限制单次等待；父进程只限制单个写者连续没有提交或退出的时长。
func TestFileSessionProcessSlowAtomicCounter(t *testing.T) {
	if mode, enabled := os.LookupEnv(fileProcessWorkerModeEnv); enabled {
		if mode != fileProcessSlowWorkerMode {
			t.Fatalf("测试子进程模式非法: %q", mode)
		}
		fileProcessRunSlowCounterWorker(t)
		return
	}

	driver, directory := newTestFileDriver(t)
	const sessionID = "process-slow-atomic-counter"
	budget := t.Context()
	if deadline, ok := t.Deadline(); ok {
		// 避免包级超时先杀死父进程，留出回收 Windows 子进程和输出管道的时间。
		var stop context.CancelFunc
		budget, stop = context.WithDeadlineCause(budget, deadline.Add(-fileProcessReapReserve),
			errors.New("包级测试时限即将到达，开始回收慢更新子进程"))
		defer stop()
	}
	ctx, cancel := context.WithCancelCause(budget)
	defer cancel(nil)
	children := make([]*fileProcessSlowChild, 0, fileProcessWorkerCount)
	t.Cleanup(func() {
		cancel(nil)
		for _, child := range children {
			_ = child.input.Close()
		}
		fileProcessWaitSlowChildren(children)
		for _, child := range children {
			t.Logf("子进程 %d 已报告 %d/%d 次成功提交", child.transcript.index, child.output.completed, fileProcessUpdatesPerWorker)
		}
		t.Logf("各进程均为单写者。回调前包括内核锁排队、身份校验和读取；回调后包括写入、Sync、替换和解锁。调度延迟可落在任一区段。\n%s",
			fileProcessChildDiagnostics(fileProcessSlowTranscripts(children)))
	})

	startup, stopStartup := context.WithTimeout(ctx, fileProcessTimeout)
	defer stopStartup()
	for index := 0; index < fileProcessWorkerCount; index++ {
		child := &fileProcessSlowChild{
			transcript: &fileProcessChild{index: index},
			done:       make(chan struct{}),
		}
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFileSessionProcessSlowAtomicCounter$", "-test.v")
		child.transcript.command = command
		command.Env = fileProcessWorkerEnvironment(directory, sessionID, fileProcessUpdatesPerWorker)
		for i, entry := range command.Env {
			name, _, _ := strings.Cut(entry, "=")
			if name == fileProcessWorkerModeEnv {
				command.Env[i] = fileProcessWorkerModeEnv + "=" + fileProcessSlowWorkerMode
			}
		}
		child.output = newFileProcessSlowOutput(&child.transcript.stdout)
		command.Stdout = child.output
		command.Stderr = &child.transcript.stderr
		var err error
		child.input, err = command.StdinPipe()
		if err != nil {
			t.Fatalf("创建第 %d 个慢更新子进程启动管道失败: %v", index, err)
		}
		if err := command.Start(); err != nil {
			_ = child.input.Close()
			t.Fatalf("启动第 %d 个慢更新子进程失败: %v", index, err)
		}
		children = append(children, child)
		go func() {
			// Wait 同时等待 stdout/stderr 复制完成，再发布退出状态和完整记录。
			child.transcript.waitErr = command.Wait()
			close(child.done)
		}()
	}

	for _, child := range children {
		select {
		case <-child.output.ready:
		case <-child.done:
			t.Fatalf("第 %d 个慢更新子进程在就绪前退出: %v", child.transcript.index, child.transcript.waitErr)
		case <-startup.Done():
			t.Fatalf("等待慢更新子进程全部就绪失败: %v", context.Cause(startup))
		}
	}
	stopStartup()
	for _, child := range children {
		if _, err := io.WriteString(child.input, fileProcessSlowStartLine); err != nil {
			t.Fatalf("放行第 %d 个慢更新子进程失败: %v", child.transcript.index, err)
		}
		_ = child.input.Close()
	}
	var monitors sync.WaitGroup
	for _, child := range children {
		monitors.Go(func() {
			if !fileProcessWaitForProgress(child.output.progress, child.done) {
				cancel(fmt.Errorf("子进程 %d 连续 %s 未报告成功提交或退出", child.transcript.index, fileProcessSlowIdleTimeout))
			}
		})
	}
	monitors.Wait()
	failed := fileProcessWaitSlowChildren(children)
	if failed || ctx.Err() != nil {
		t.Fatalf("慢更新跨进程原子计数失败: child_failed=%t context=%v\n%s",
			failed, context.Cause(ctx), fileProcessLockDiagnostics(driver, sessionID))
	}
	for _, child := range children {
		if child.output.completed != fileProcessUpdatesPerWorker {
			t.Fatalf("第 %d 个子进程的提交记录不完整: actual=%d expected=%d",
				child.transcript.index, child.output.completed, fileProcessUpdatesPerWorker)
		}
	}
	content, found, err := driver.Read(sessionID)
	if err != nil || !found {
		t.Fatalf("父进程读取慢更新 Session 失败: found=%t err=%v", found, err)
	}
	count, err := fileProcessDecodeCounter(content)
	if err != nil {
		t.Fatalf("父进程解码慢更新 Session 失败: %v", err)
	}
	if expected := fileProcessWorkerCount * fileProcessUpdatesPerWorker; count != expected {
		t.Fatalf("慢更新跨进程原子计数不精确: actual=%d expected=%d", count, expected)
	}
}

func fileProcessRunSlowCounterWorker(t *testing.T) {
	t.Helper()
	directory := fileProcessRequiredEnvironment(t, fileProcessSessionDirectoryEnv)
	id := fileProcessRequiredEnvironment(t, fileProcessSessionIDEnv)
	if updates := fileProcessRequiredEnvironment(t, fileProcessUpdateCountEnv); updates != strconv.Itoa(fileProcessUpdatesPerWorker) {
		t.Fatalf("慢更新子进程必须执行 %d 轮，实际为 %q", fileProcessUpdatesPerWorker, updates)
	}
	driver, err := NewFile(directory)
	if err != nil {
		t.Fatalf("创建慢更新子进程 File 驱动失败: %v", err)
	}
	if err := validateSessionID(id); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(os.Stdout, fileProcessSlowReadyLine); err != nil {
		t.Fatalf("慢更新子进程报告就绪失败: %v", err)
	}
	start, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil || start != fileProcessSlowStartLine {
		t.Fatalf("慢更新子进程启动握手失败: command=%q err=%v", start, err)
	}

	// 容量覆盖完整批次，写者释放锁后只投递快照；格式化和管道写入不阻塞下一轮竞争。
	commits := make(chan fileProcessUpdateTiming, fileProcessUpdatesPerWorker)
	reported := make(chan error, 1)
	go func() {
		round := 1
		for timing := range commits {
			if _, err := io.WriteString(os.Stdout, timing.diagnosticLine(round)); err != nil {
				reported <- err
				return
			}
			round++
		}
		reported <- nil
	}()
	defer func() {
		close(commits)
		if err := <-reported; err != nil {
			t.Errorf("报告慢更新进度失败: %v", err)
		}
	}()
	run := func(update func(string, bool) (string, bool, error)) error { return driver.Update(id, update) }
	for index := 0; index < fileProcessUpdatesPerWorker; index++ {
		timing, err := fileProcessMeasureUpdate(run, func(current string, found bool) (string, bool, error) {
			next, remove, err := fileProcessIncrementEnvelope(current, found)
			// 持锁回调内仅做业务更新与固定等待。
			time.Sleep(fileProcessSlowHold)
			return next, remove, err
		}, time.Now)
		if err != nil {
			t.Fatalf("慢更新子进程第 %d 次原子更新失败，生产锁等待超时=%t:\n%s%s",
				index+1, errors.Is(err, ErrSessionLockTimeout), timing.diagnosticLine(index+1), fileProcessErrorChain(err))
		}
		commits <- timing
	}
}

type fileProcessSlowChild struct {
	transcript *fileProcessChild
	input      io.WriteCloser
	output     *fileProcessSlowOutput
	done       chan struct{}
}

func fileProcessWaitSlowChildren(children []*fileProcessSlowChild) bool {
	failed := false
	for _, child := range children {
		<-child.done
		failed = failed || child.transcript.waitErr != nil
	}
	return failed
}

func fileProcessSlowTranscripts(children []*fileProcessSlowChild) []*fileProcessChild {
	transcripts := make([]*fileProcessChild, len(children))
	for i, child := range children {
		transcripts[i] = child.transcript
	}
	return transcripts
}
