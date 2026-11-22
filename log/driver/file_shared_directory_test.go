package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/internal/logcoord"
	"github.com/zhuhanxin0308/thinkgo/v3/log"
)

const sharedFilePeerDirectory = "THINKGO_TEST_SHARED_LOG_DIRECTORY"

type sharedLogWrite struct {
	Time          time.Time
	Message       string
	Batch         bool
	HoldDirectory bool
}

func (write sharedLogWrite) entry() *log.LogEntry {
	return &log.LogEntry{Time: write.Time, Level: "info", Message: write.Message}
}

func (write sharedLogWrite) appendTo(driver *File) error {
	if write.Batch {
		return driver.SaveEntries([]*log.LogEntry{write.entry()})
	}
	return driver.WriteEntry(write.entry())
}

// TestSharedLogPeer 在独立进程内保留真实驱动和句柄，逐条报告同步写入结果。
func TestSharedLogPeer(t *testing.T) {
	directory, enabled := os.LookupEnv(sharedFilePeerDirectory)
	if !enabled {
		t.Skip("仅在共享目录测试的子进程中运行")
	}
	in, out := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	var options FileOptions
	if err := in.Decode(&options); err != nil {
		t.Fatal(err)
	}
	driver, err := NewFileWithOptions(directory, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	for {
		var write sharedLogWrite
		if err := in.Decode(&write); errors.Is(err, io.EOF) {
			return
		} else if err != nil {
			t.Fatal(err)
		}
		if write.HoldDirectory {
			err := withLogDirectoryLock(directory, nil, func() error {
				if err := out.Encode("持有目录锁"); err != nil {
					return err
				}
				var release struct{}
				return in.Decode(&release)
			})
			if err != nil && !errors.Is(err, io.EOF) {
				t.Fatal(err)
			}
			return
		}
		var message string
		if err := write.appendTo(driver); err != nil {
			message = err.Error()
		}
		if err := out.Encode(message); err != nil {
			t.Fatal(err)
		}
	}
}

type sharedLogPeer struct {
	command *exec.Cmd
	input   io.WriteCloser
	writes  *json.Encoder
	results *json.Decoder
	stderr  bytes.Buffer
}

func startSharedLogPeer(t *testing.T, directory string, options FileOptions) *sharedLogPeer {
	t.Helper()
	const processLimit = 30 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), processLimit)
	t.Cleanup(cancel)
	peer := &sharedLogPeer{command: exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSharedLogPeer$")}
	peer.command.Env = append(os.Environ(), sharedFilePeerDirectory+"="+directory)
	peer.command.Stderr = &peer.stderr
	input, err := peer.command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := peer.command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	peer.input, peer.writes, peer.results = input, json.NewEncoder(input), json.NewDecoder(output)
	if err := peer.command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if peer.command != nil {
			_ = peer.input.Close()
			if err := peer.command.Wait(); err != nil {
				t.Errorf("日志子进程退出失败: %v %s", err, peer.stderr.String())
			}
		}
	})
	if err := peer.writes.Encode(options); err != nil {
		t.Fatal(err)
	}
	return peer
}

func (peer *sharedLogPeer) write(t *testing.T, write sharedLogWrite) string {
	t.Helper()
	if err := peer.writes.Encode(write); err != nil {
		t.Fatal(err)
	}
	var message string
	if err := peer.results.Decode(&message); err != nil {
		t.Fatalf("读取子进程写入结果失败: %v", err)
	}
	return message
}

func (peer *sharedLogPeer) kill(t *testing.T) {
	t.Helper()
	if err := peer.command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := peer.command.Wait(); err == nil {
		t.Fatal("子进程应被强制终止")
	}
	_ = peer.input.Close()
	peer.command = nil
}

func TestSharedLogDirectoryProtectsOpenFilesAcrossProcesses(t *testing.T) {
	now := time.Now().UTC()
	old := sharedLogWrite{Time: now.AddDate(0, 0, -3), Message: strings.Repeat("abcdefgh", 32)}
	for _, test := range []struct {
		name     string
		options  FileOptions
		capacity bool
	}{
		{"文件数", FileOptions{MaxFiles: 1}, true},
		{"总大小", FileOptions{MaxTotalSize: int64(len(old.entry().FormatEntry()) + 1)}, true},
		{"保留天数", FileOptions{RetentionDays: 1}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			peer := startSharedLogPeer(t, directory, test.options)
			if msg := peer.write(t, old); msg != "" {
				t.Fatalf("子进程初次写入失败: %s", msg)
			}
			driver, err := NewFileWithOptions(directory, test.options)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = driver.Close() })
			fresh := sharedLogWrite{Time: now, Message: "新日期", Batch: true}
			err = fresh.appendTo(driver)
			if test.capacity && (err == nil || !strings.Contains(err.Error(), "无可清理文件")) {
				t.Errorf("全部候选文件正在使用时，容量冲突应可观察: %v", err)
			} else if !test.capacity && err != nil {
				t.Errorf("到期清理应跳过仍在使用的候选: %v", err)
			}
			oldPath := filepath.Join(directory, old.Time.Format("2006-01-02")+".log")
			content, err := os.ReadFile(oldPath)
			if err != nil || !bytes.Contains(content, []byte(old.Message)) {
				t.Errorf("清理使子进程仍持有的文件不可读: %v", err)
			}
			late := sharedLogWrite{Time: old.Time, Message: "延迟到达", Batch: true}
			if msg := peer.write(t, late); msg != "" && !strings.Contains(msg, "无可清理文件") {
				t.Fatalf("子进程无法继续追加: %s", msg)
			}
			content, err = os.ReadFile(oldPath)
			if err != nil || !bytes.Contains(content, []byte(late.Message)) {
				t.Errorf("追加的数据没有出现在日志路径: %v", err)
			}
			peer.kill(t)
			if err := fresh.appendTo(driver); err != nil {
				t.Fatalf("持有进程退出后无法再次写入并清理: %v", err)
			}
			if _, err := os.Stat(oldPath); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("持有进程退出后应回收旧日志: %v", err)
			}
		})
	}
}

func TestSharedLogDirectoryRotatesUsingActualSize(t *testing.T) {
	directory := t.TempDir()
	write := sharedLogWrite{Time: time.Now().UTC(), Message: "固定长度"}
	const linesPerFile = 2
	options := FileOptions{MaxFileSize: int64(len(write.entry().FormatEntry())+1) * linesPerFile}
	driver, err := NewFileWithOptions(directory, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	if err := write.appendTo(driver); err != nil {
		t.Fatal(err)
	}
	peer := startSharedLogPeer(t, directory, options)
	if msg := peer.write(t, write); msg != "" {
		t.Fatal(msg)
	}
	write.Batch = true
	if err := write.appendTo(driver); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(directory, write.Time.Format("2006-01-02"))
	for path, want := range map[string]int{base + ".log": linesPerFile, base + "_1.log": 1} {
		content, err := os.ReadFile(path)
		if err != nil || bytes.Count(content, []byte("\n")) != want {
			t.Errorf("应有 %d 行完整日志: path=%s err=%v content=%q", want, path, err, content)
		}
	}
}

func TestSharedLogReplacesStaleCachedHandle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 缓存句柄拒绝外部重命名")
	}
	directory := t.TempDir()
	driver := NewFile(directory)
	t.Cleanup(func() { _ = driver.Close() })
	write := sharedLogWrite{Time: time.Now().UTC(), Message: "原始文件"}
	if err := write.appendTo(driver); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, write.Time.Format("2006-01-02")+".log")
	if err := os.Rename(path, path+".archive"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("新文件\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	write.Message = "应写入新文件"
	if err := write.appendTo(driver); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(content, []byte(write.Message)) {
		t.Fatalf("成功报告的日志未写入替换后的真实路径: %v %q", err, content)
	}
}

func TestSharedLogDirectoryWriteWaitsForExitedOwner(t *testing.T) {
	directory := t.TempDir()
	peer := startSharedLogPeer(t, directory, FileOptions{})
	if got := peer.write(t, sharedLogWrite{HoldDirectory: true}); got != "持有目录锁" {
		t.Fatalf("子进程未取得实际目录锁: %q", got)
	}
	driver := NewFile(directory)
	started, done := make(chan struct{}), make(chan error, 1)
	go func() {
		close(started)
		err := (sharedLogWrite{Time: time.Now(), Message: "等待持锁进程退出"}).appendTo(driver)
		done <- errors.Join(err, driver.Close())
	}()
	<-started
	// 子进程在锁内发送应答；保持锁一段时间，验证普通公开写入会等待真实持有者。
	const observation = 100 * time.Millisecond
	select {
	case err := <-done:
		t.Fatalf("其他进程仍持目录锁时已结束写入: %v", err)
	case <-time.After(observation):
	}
	peer.kill(t)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("子进程退出后目录锁未恢复: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("目录锁持有者退出后等待者仍未完成")
	}
}

func TestSharedLogDirectoryRemovesOldestIdleCandidate(t *testing.T) {
	now := time.Now().UTC()
	directory := t.TempDir()
	options := FileOptions{MaxFiles: 2}
	first, err := NewFileWithOptions(directory, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	old := sharedLogWrite{Time: now.AddDate(0, 0, -3), Message: "同进程其他通道"}
	if err := old.appendTo(first); err != nil {
		t.Fatal(err)
	}
	idle := writeTestLogFile(t, directory, now.AddDate(0, 0, -2), "", 1)
	second, err := NewFileWithOptions(directory, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if err := (sharedLogWrite{Time: now, Message: "新通道"}).appendTo(second); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(idle); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("没有删除最旧的空闲候选: %v", err)
	}
	if err := old.appendTo(first); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(directory, old.Time.Format("2006-01-02")+".log"))
	if err != nil || bytes.Count(content, []byte(old.Message)) != 2 {
		t.Fatalf("活动文件的历史或追加丢失: %v %q", err, content)
	}
}

func TestSharedLogCleanupRefusesReplacedCandidate(t *testing.T) {
	directory := t.TempDir()
	path := writeTestLogFile(t, directory, time.Now(), "", 1)
	candidates, err := NewFile(directory).managedLogFilesLocked(time.UTC)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("未能记录原文件的清理快照: %v %v", candidates, err)
	}
	if err := os.Rename(path, path+".archive"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("替换后的日志"), 0o600); err != nil {
		t.Fatal(err)
	}
	removed, err := removeIdleLogFile(candidates[0])
	if removed || !errors.Is(err, errUnsafeLogFilePath) {
		t.Fatalf("过时的清理快照没有被拒绝: removed=%t err=%v", removed, err)
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != "替换后的日志" {
		t.Fatalf("错误删除了同名替换文件: %v %q", err, content)
	}
}

func TestConfiguredLogDirectoryAliasSharesRetention(t *testing.T) {
	directory := t.TempDir()
	alias := filepath.Join(t.TempDir(), "configured-logs")
	if err := os.Symlink(directory, alias); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("当前 Windows 用户无法创建目录链接: %v", err)
		}
		t.Fatal(err)
	}
	options := FileOptions{MaxFiles: 1}
	first, err := NewFileWithOptions(alias, options)
	if err != nil {
		t.Fatalf("合法的配置目录别名必须可用: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })
	now := time.Now().UTC()
	old := sharedLogWrite{Time: now.AddDate(0, 0, -1), Message: "通过别名"}
	if err := old.appendTo(first); err != nil {
		t.Fatal(err)
	}
	second, err := NewFileWithOptions(directory, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if err := (sharedLogWrite{Time: now, Message: "真实目录"}).appendTo(second); err == nil {
		t.Fatal("同一实际目录的日志治理应保护通过别名写入的活动文件")
	}
}

func TestLogOwnerMetadataRetiresAfterLastCloseAndCrash(t *testing.T) {
	directory := t.TempDir()
	first, second := NewFile(directory), NewFile(directory)
	t.Cleanup(func() { _ = first.Close(); _ = second.Close() })
	now := time.Now().UTC()
	write := sharedLogWrite{Time: now, Message: "同一文件的两个持有者"}
	if err := write.appendTo(first); err != nil {
		t.Fatal(err)
	}
	if err := write.appendTo(second); err != nil {
		t.Fatal(err)
	}
	sidecar := logcoord.OwnerPath(filepath.Join(directory, now.Format(time.DateOnly)+".log"))
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(sidecar); err != nil {
		t.Fatalf("共享者尚未关闭时丢失了所有权记录: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(sidecar); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("最后一个持有者关闭后应立即回收侧文件: %v", err)
	}
	orphan := logcoord.OwnerPath(filepath.Join(directory, now.AddDate(0, 0, -2).Format(time.DateOnly)+".log"))
	foreign := logcoord.OwnerPath(filepath.Join(directory, now.AddDate(0, 0, -3).Format(time.DateOnly)+".log"))
	if err := os.WriteFile(orphan, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(foreign, []byte("已有业务内容"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := write.appendTo(first); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("崩溃或行政清理后的孤立侧文件未回收: %v", err)
	}
	if data, err := os.ReadFile(foreign); err != nil || string(data) != "已有业务内容" {
		t.Fatalf("与协调文件同名的业务内容没有保留: %q %v", data, err)
	}
}

func TestBoundLogDirectoryRefusesReplacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 上打开的日志默认禁止重命名")
	}
	root := t.TempDir()
	directory := filepath.Join(root, "logs")
	driver, err := NewFileWithOptions(directory, FileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	write := sharedLogWrite{Time: time.Now().UTC(), Message: "原目录"}
	if err := write.appendTo(driver); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(directory, filepath.Join(root, "moved")); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := os.Symlink(target, directory); err != nil {
		t.Fatal(err)
	}
	if err := write.appendTo(driver); err == nil {
		t.Error("已经绑定的日志位置不能被外部链接重定向")
	}
	if err := driver.Close(); err == nil || driver.currentFile != nil || driver.currentOwner != nil {
		t.Fatalf("目录被替换时关闭应报错并释放原始句柄: %v", err)
	}
	entries, err := os.ReadDir(target)
	if err != nil || len(entries) != 0 {
		t.Fatalf("不能在替换目标内创建、锁定或清理文件: %v %v", entries, err)
	}
}

func TestLegacyRotationNamesDoNotClaimSimilarOwnerFiles(t *testing.T) {
	directory := t.TempDir()
	now := time.Now().UTC()
	legacy := map[string]string{"_01": "既有业务内容", "_+1": ""}
	for suffix, payload := range legacy {
		path := writeTestLogFile(t, directory, now.AddDate(0, 0, -3), suffix, 1)
		foreign := logcoord.OwnerPath(path)
		if err := os.WriteFile(foreign, []byte(payload), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	driver, err := NewFileWithOptions(directory, FileOptions{RetentionDays: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	if err := (sharedLogWrite{Time: now, Message: "治理历史段"}).appendTo(driver); err != nil {
		t.Fatal(err)
	}
	for suffix, payload := range legacy {
		path := filepath.Join(directory, now.AddDate(0, 0, -3).Format(time.DateOnly)+suffix+".log")
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("旧格式日志没有按天清理: %s %v", path, err)
		}
		if got, err := os.ReadFile(logcoord.OwnerPath(path)); err != nil || string(got) != payload {
			t.Errorf("不属于驱动的文件被改动: %s %q %v", path, got, err)
		}
	}
}
