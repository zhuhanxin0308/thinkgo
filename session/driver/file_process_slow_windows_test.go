//go:build windows

package driver

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	fileProcessSlowWorkerMode = "slow-atomic-counter"
	fileProcessSlowReadyLine  = "thinkgo-slow-counter-ready\n"
	fileProcessSlowStartLine  = "start\n"
	fileProcessSlowHold       = 125 * time.Millisecond
)

// TestFileSessionProcessSlowAtomicCounter keeps the original 8 x 25 updates,
// 15-second production lock wait and 45-second parent deadline. The callback's
// fixed hold models slow work inside the mutation boundary: 25 seconds of total
// ownership, while a fair first turn needs roughly eight holds, not 25 seconds.
// Every child finishes initialization before the parent releases the start gate.
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
	ctx, cancel := context.WithTimeout(context.Background(), fileProcessTimeout)
	defer cancel()
	children := make([]*fileProcessSlowChild, 0, fileProcessWorkerCount)
	t.Cleanup(func() {
		cancel()
		for _, child := range children {
			_ = child.input.Close()
		}
		fileProcessWaitSlowChildren(children)
		if t.Failed() {
			t.Logf("全部慢更新子进程诊断:\n%s", fileProcessChildDiagnostics(fileProcessSlowTranscripts(children)))
		}
	})

	for index := 0; index < fileProcessWorkerCount; index++ {
		child := &fileProcessSlowChild{
			transcript: &fileProcessChild{index: index},
			ready:      make(chan struct{}),
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
		command.Stdout = &fileProcessSlowReadyWriter{output: &child.transcript.stdout, ready: child.ready}
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
			// Wait also joins stdout/stderr copying before publishing the transcript.
			child.transcript.waitErr = command.Wait()
			close(child.done)
		}()
	}

	for _, child := range children {
		select {
		case <-child.ready:
		case <-child.done:
			t.Fatalf("第 %d 个慢更新子进程在就绪前退出: %v", child.transcript.index, child.transcript.waitErr)
		case <-ctx.Done():
			t.Fatalf("等待慢更新子进程全部就绪失败: %v", ctx.Err())
		}
	}
	for _, child := range children {
		if _, err := io.WriteString(child.input, fileProcessSlowStartLine); err != nil {
			t.Fatalf("放行第 %d 个慢更新子进程失败: %v", child.transcript.index, err)
		}
		_ = child.input.Close()
	}
	if failed := fileProcessWaitSlowChildren(children); failed || ctx.Err() != nil {
		t.Fatalf("慢更新跨进程原子计数失败: child_failed=%t context=%v\n%s",
			failed, ctx.Err(), fileProcessLockDiagnostics(driver, sessionID))
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
	if updates := fileProcessRequiredEnvironment(t, fileProcessUpdateCountEnv); updates != "25" {
		t.Fatalf("慢更新子进程必须执行原 25 轮，实际为 %q", updates)
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

	timings := make([]fileProcessUpdateTiming, 0, fileProcessUpdatesPerWorker)
	defer func() {
		t.Logf("慢更新阶段时序（callback 固定持锁 %s；其余阶段不是纯锁等待／持锁时间）:\n%s",
			fileProcessSlowHold, fileProcessTimingDiagnostics(timings))
	}()
	run := func(update func(string, bool) (string, bool, error)) error { return driver.Update(id, update) }
	for index := 0; index < fileProcessUpdatesPerWorker; index++ {
		timing, err := fileProcessMeasureUpdate(run, func(current string, found bool) (string, bool, error) {
			next, remove, err := fileProcessIncrementEnvelope(current, found)
			// No logging or IPC inside the held mutation guard.
			time.Sleep(fileProcessSlowHold)
			return next, remove, err
		}, time.Now)
		timings = append(timings, timing)
		if err != nil {
			t.Fatalf("慢更新子进程第 %d 次原子更新失败，Update 总耗时 %s:\n%s",
				index+1, timing.finished.Sub(timing.started), fileProcessErrorChain(err))
		}
	}
}

type fileProcessSlowChild struct {
	transcript *fileProcessChild
	input      io.WriteCloser
	ready      chan struct{}
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

// Ready detection shares the stdout copier with transcript retention. Unlike a
// StdoutPipe scanner followed by Wait, Wait cannot close the pipe before the
// remaining child output is copied. The marker may span arbitrary Write chunks.
type fileProcessSlowReadyWriter struct {
	output io.Writer
	ready  chan struct{}
	once   sync.Once
	tail   string
}

func (writer *fileProcessSlowReadyWriter) Write(data []byte) (int, error) {
	n, err := writer.output.Write(data)
	combined := writer.tail + string(data[:n])
	if strings.Contains(combined, fileProcessSlowReadyLine) {
		writer.once.Do(func() { close(writer.ready) })
	}
	if limit := len(fileProcessSlowReadyLine) - 1; len(combined) > limit {
		writer.tail = combined[len(combined)-limit:]
	} else {
		writer.tail = combined
	}
	return n, err
}
