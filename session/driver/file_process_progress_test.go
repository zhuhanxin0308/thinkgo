package driver

import (
	"bytes"
	"io"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

const (
	fileProcessSlowReadyLine = "thinkgo-slow-counter-ready\n"
	// 单个写者要有时间报告生产锁截止以及文件 I/O、调度和输出带来的延迟。
	fileProcessSlowIdleTimeout = 3 * fileSessionLockWait
)

// 写者只发送成功提交的时序。stdout 复制协程独占缓冲区，父进程在 Wait 后读取。
type fileProcessSlowOutput struct {
	output    *bytes.Buffer
	ready     chan struct{}
	progress  chan struct{}
	consumed  int
	completed int
}

func newFileProcessSlowOutput(output *bytes.Buffer) *fileProcessSlowOutput {
	return &fileProcessSlowOutput{output: output, ready: make(chan struct{}), progress: make(chan struct{}, 1)}
}

func (writer *fileProcessSlowOutput) Write(data []byte) (int, error) {
	n, err := writer.output.Write(data)
	unread := writer.output.Bytes()[writer.consumed:]
	for {
		end := bytes.IndexByte(unread, '\n')
		if end < 0 {
			return n, err
		}
		line := string(unread[:end+1])
		writer.consumed += end + 1
		unread = unread[end+1:]
		if line == fileProcessSlowReadyLine {
			select {
			case <-writer.ready:
			default:
				close(writer.ready)
			}
		} else if writer.completed < fileProcessUpdatesPerWorker &&
			strings.HasPrefix(line, "update="+strconv.Itoa(writer.completed+1)+" start=") {
			writer.completed++
			// 同一批 stdout 可携带多次提交；唤醒只需一次，每轮时序仍留在原始输出中。
			select {
			case writer.progress <- struct{}{}:
			default:
			}
		}
	}
}

// 每个写者单独计时，健康写者的吞吐不能掩盖另一个写者停滞。
func fileProcessWaitForProgress(progress, done <-chan struct{}) bool {
	timer := time.NewTimer(fileProcessSlowIdleTimeout)
	defer timer.Stop()
	for {
		select {
		case <-done:
			return true
		case <-progress:
		case <-timer.C:
			// 父进程恢复调度时，已到达的提交或进程退出应先于超时处理。
			select {
			case <-done:
				return true
			case <-progress:
			default:
				return false
			}
		}
		timer.Reset(fileProcessSlowIdleTimeout)
	}
}

// 用虚拟时钟重放整批超过旧截止、每个写者仍持续提交的情形。
func TestFileProcessSlowProgressOutlivesBatchDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var transcript bytes.Buffer
		writer := newFileProcessSlowOutput(&transcript)
		done := make(chan struct{})
		finished := make(chan bool, 1)
		go func() { finished <- fileProcessWaitForProgress(writer.progress, done) }()
		for round := 1; round <= fileProcessUpdatesPerWorker; round++ {
			time.Sleep(fileProcessSlowIdleTimeout / 2)
			now := time.Now()
			timing := fileProcessUpdateTiming{started: now, callbackStarted: now, callbackFinished: now, finished: now}
			line := timing.diagnosticLine(round)
			// stdout 复制可在任意字节拆包，只有完整的提交行才构成进度。
			io.WriteString(writer, line[:len(line)-1])
			io.WriteString(writer, "\n")
			synctest.Wait()
			select {
			case <-finished:
				t.Fatalf("第 %d 轮仍在推进时提前截止", round)
			default:
			}
		}
		close(done)
		if !<-finished || writer.completed != fileProcessUpdatesPerWorker {
			t.Fatalf("进程退出后应保留完整的 %d 次提交，实际 %d", fileProcessUpdatesPerWorker, writer.completed)
		}
	})
}

// 非进度输出、重复序号、半行及其他子进程的提交均不能为停滞写者续期。
func TestFileProcessSlowProgressDetectsStalledWorker(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var stalledLog, peerLog bytes.Buffer
		stalled, peer := newFileProcessSlowOutput(&stalledLog), newFileProcessSlowOutput(&peerLog)
		done := make(chan struct{})
		finished := make(chan bool, 1)
		go func() { finished <- fileProcessWaitForProgress(stalled.progress, done) }()
		now := time.Now()
		timing := fileProcessUpdateTiming{started: now, callbackStarted: now, callbackFinished: now, finished: now}
		first := timing.diagnosticLine(1)
		io.WriteString(stalled, strings.TrimSuffix(fileProcessSlowReadyLine, "\n"))
		io.WriteString(stalled, "\n"+first)
		synctest.Wait()
		select {
		case <-stalled.ready:
		default:
			t.Fatal("一次读取含就绪和提交行时丢失了启动信号")
		}
		time.Sleep(fileProcessSlowIdleTimeout / 2)
		noise := fileProcessSlowReadyLine + first + "普通诊断\n" + timing.diagnosticLine(3) + "update=2 start="
		io.WriteString(stalled, noise)
		io.WriteString(peer, first)
		time.Sleep(fileProcessSlowIdleTimeout / 2)
		synctest.Wait()
		select {
		case ok := <-finished:
			if ok {
				t.Fatal("停滞的进程被报告为正常结束")
			}
		default:
			t.Fatal("无进展写者的独立截止被推迟")
		}
		if stalled.completed != 1 || stalledLog.String() != fileProcessSlowReadyLine+first+noise {
			t.Fatalf("停滞前进度或原始输出丢失: completed=%d output=%q", stalled.completed, stalledLog.String())
		}
	})
}
