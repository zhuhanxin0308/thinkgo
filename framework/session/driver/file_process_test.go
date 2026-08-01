package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	fileProcessWorkerModeEnv       = "THINKGO_FILE_SESSION_PROCESS_WORKER"
	fileProcessSessionDirectoryEnv = "THINKGO_FILE_SESSION_PROCESS_DIRECTORY"
	fileProcessSessionIDEnv        = "THINKGO_FILE_SESSION_PROCESS_ID"
	fileProcessUpdateCountEnv      = "THINKGO_FILE_SESSION_PROCESS_UPDATES"
	fileProcessWorkerMode          = "atomic-counter"
	fileProcessWorkerCount         = 8
	fileProcessUpdatesPerWorker    = 25
	fileProcessMaximumUpdates      = 1000
	fileProcessTimeout             = 45 * time.Second
	fileProcessEnvelopeVersion     = 1
	fileProcessCounterKey          = "counter"
)

// fileProcessSessionEnvelope 表示用于验证跨进程原子更新的真实 Session JSON 信封结构。
type fileProcessSessionEnvelope struct {
	Version int                        `json:"version"`
	Data    map[string]json.RawMessage `json:"data"`
}

// fileProcessChild 保存一个已启动的测试子进程及其独立的诊断输出。
type fileProcessChild struct {
	index   int
	command *exec.Cmd
	stdout  bytes.Buffer
	stderr  bytes.Buffer
}

// TestFileSessionProcessAtomicCounter 使用当前测试二进制验证真实文件锁在多个进程间的原子计数语义。
func TestFileSessionProcessAtomicCounter(t *testing.T) {
	if mode, enabled := os.LookupEnv(fileProcessWorkerModeEnv); enabled {
		if mode != fileProcessWorkerMode {
			t.Fatalf("测试子进程模式非法: %q", mode)
		}
		fileProcessRunAtomicCounterWorker(t)
		return
	}

	driver, directory := newTestFileDriver(t)
	const sessionID = "process-atomic-counter"
	contextWithTimeout, cancel := context.WithTimeout(context.Background(), fileProcessTimeout)
	defer cancel()

	children := make([]*fileProcessChild, 0, fileProcessWorkerCount)
	for index := 0; index < fileProcessWorkerCount; index++ {
		child := &fileProcessChild{index: index}
		child.command = exec.CommandContext(
			contextWithTimeout,
			os.Args[0],
			"-test.run=^TestFileSessionProcessAtomicCounter$",
			"-test.v",
		)
		child.command.Env = fileProcessWorkerEnvironment(directory, sessionID, fileProcessUpdatesPerWorker)
		child.command.Stdout = &child.stdout
		child.command.Stderr = &child.stderr
		if err := child.command.Start(); err != nil {
			cancel()
			for _, started := range children {
				_ = started.command.Wait()
			}
			t.Fatalf("启动第 %d 个跨进程测试子进程失败: %v", index, err)
		}
		children = append(children, child)
	}
	var failures []string
	for _, child := range children {
		if err := child.command.Wait(); err != nil {
			failures = append(failures, fmt.Sprintf(
				"子进程 %d 失败: %v\nstdout:\n%s\nstderr:\n%s",
				child.index,
				err,
				child.stdout.String(),
				child.stderr.String(),
			))
		}
	}
	if contextErr := contextWithTimeout.Err(); contextErr != nil {
		failures = append(failures, fmt.Sprintf("等待跨进程测试子进程超时: %v", contextErr))
	}
	if len(failures) != 0 {
		failures = append(failures, fileProcessLockDiagnostics(driver, sessionID))
		t.Fatalf("跨进程原子计数失败:\n%s", strings.Join(failures, "\n"))
	}

	content, found, err := driver.Read(sessionID)
	if err != nil || !found {
		t.Fatalf("父进程通过真实 File.Read 读取 Session 信封失败: found=%t err=%v", found, err)
	}
	count, err := fileProcessDecodeCounter(content)
	if err != nil {
		t.Fatalf("父进程通过真实 File.Read 解码 Session 信封失败: %v", err)
	}
	expected := fileProcessWorkerCount * fileProcessUpdatesPerWorker
	if count != expected {
		t.Fatalf("跨进程原子计数不精确: actual=%d expected=%d", count, expected)
	}

	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("读取 Session 目录以检查残留锁失败: %v", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".lock") {
			t.Fatalf("跨进程更新后遗留锁文件: %s", filepath.Join(directory, entry.Name()))
		}
	}
}

// fileProcessRunAtomicCounterWorker 严格读取父进程参数，并在独立 File 实例上执行多次原子更新。
func fileProcessRunAtomicCounterWorker(t *testing.T) {
	directory := fileProcessRequiredEnvironment(t, fileProcessSessionDirectoryEnv)
	sessionID := fileProcessRequiredEnvironment(t, fileProcessSessionIDEnv)
	updatesText := fileProcessRequiredEnvironment(t, fileProcessUpdateCountEnv)
	updates, err := strconv.Atoi(updatesText)
	if err != nil || updates <= 0 || updates > fileProcessMaximumUpdates {
		t.Fatalf("测试子进程更新次数非法: %q", updatesText)
	}
	driver, err := NewFile(directory)
	if err != nil {
		t.Fatalf("测试子进程创建独立 File 驱动失败: %v", err)
	}
	if err = validateSessionID(sessionID); err != nil {
		t.Fatalf("测试子进程 Session ID 非法: %v", err)
	}
	for index := 0; index < updates; index++ {
		started := time.Now()
		if err = driver.Update(sessionID, fileProcessIncrementEnvelope); err != nil {
			t.Fatalf("测试子进程第 %d 次原子更新失败，Update 总耗时 %s:\n%s", index+1, time.Since(started), fileProcessErrorChain(err))
		}
	}
}

// fileProcessLockDiagnostics 采集失败时锁文件的路径、身份、时间和载荷，供定位跨进程竞争根因。
func fileProcessLockDiagnostics(driver *File, sessionID string) string {
	path := driver.lockFilePath(sessionID)
	info, statErr := os.Lstat(path)
	if statErr != nil {
		return fmt.Sprintf("失败后锁文件诊断: path=%s lstat=%s", path, fileProcessErrorChain(statErr))
	}
	data, readErr := os.ReadFile(path)
	return fmt.Sprintf(
		"失败后锁文件诊断: path=%s mode=%s size=%d mod_time=%s identity=%s payload=%q read_error=%s",
		path,
		info.Mode(),
		info.Size(),
		info.ModTime().UTC().Format(time.RFC3339Nano),
		fileProcessFileIdentity(info),
		string(data),
		fileProcessErrorChain(readErr),
	)
}

// fileProcessFileIdentity 输出测试平台可比较的文件身份摘要。
func fileProcessFileIdentity(info os.FileInfo) string {
	return fmt.Sprintf("name=%q mode=%s size=%d mod_time=%s", info.Name(), info.Mode(), info.Size(), info.ModTime().UTC().Format(time.RFC3339Nano))
}

// fileProcessErrorChain 展开 Join、PathError 等嵌套错误，保留错误类型、操作和路径。
func fileProcessErrorChain(err error) string {
	if err == nil {
		return "<nil>"
	}
	var builder strings.Builder
	fileProcessAppendError(&builder, err, "")
	return builder.String()
}

// fileProcessAppendError 递归写入单个错误节点及其所有嵌套原因。
func fileProcessAppendError(builder *strings.Builder, err error, indent string) {
	if err == nil {
		return
	}
	fmt.Fprintf(builder, "%s%T: %v", indent, err, err)
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		fmt.Fprintf(builder, " [op=%q path=%q cause=%T:%v]", pathErr.Op, pathErr.Path, pathErr.Err, pathErr.Err)
	}
	if joined, found := err.(interface{ Unwrap() []error }); found {
		for _, nested := range joined.Unwrap() {
			builder.WriteByte('\n')
			fileProcessAppendError(builder, nested, indent+"  ")
		}
		return
	}
	if nested := errors.Unwrap(err); nested != nil {
		builder.WriteByte('\n')
		fileProcessAppendError(builder, nested, indent+"  ")
	}
}

// fileProcessWorkerEnvironment 构造唯一的辅助模式环境，避免继承环境意外递归启动测试子进程。
func fileProcessWorkerEnvironment(directory, sessionID string, updates int) []string {
	environment := make([]string, 0, len(os.Environ())+4)
	for _, entry := range os.Environ() {
		name, _, found := strings.Cut(entry, "=")
		if found && (name == fileProcessWorkerModeEnv || name == fileProcessSessionDirectoryEnv ||
			name == fileProcessSessionIDEnv || name == fileProcessUpdateCountEnv) {
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment,
		fileProcessWorkerModeEnv+"="+fileProcessWorkerMode,
		fileProcessSessionDirectoryEnv+"="+directory,
		fileProcessSessionIDEnv+"="+sessionID,
		fileProcessUpdateCountEnv+"="+strconv.Itoa(updates),
	)
}

// fileProcessRequiredEnvironment 返回必填且不含首尾空白的测试子进程参数。
func fileProcessRequiredEnvironment(t *testing.T, name string) string {
	t.Helper()
	value, found := os.LookupEnv(name)
	if !found || value == "" || strings.TrimSpace(value) != value {
		t.Fatalf("测试子进程环境变量非法: %s", name)
	}
	return value
}

// fileProcessIncrementEnvelope 将 JSON 信封中的整数计数加一，并拒绝损坏或不符合格式的载荷。
func fileProcessIncrementEnvelope(current string, found bool) (string, bool, error) {
	envelope := fileProcessSessionEnvelope{
		Version: fileProcessEnvelopeVersion,
		Data:    make(map[string]json.RawMessage),
	}
	if found {
		if err := json.Unmarshal([]byte(current), &envelope); err != nil {
			return "", false, fmt.Errorf("解码 Session 信封失败: %w", err)
		}
		if envelope.Version != fileProcessEnvelopeVersion || envelope.Data == nil {
			return "", false, fmt.Errorf("Session 信封格式非法")
		}
	}
	count, err := fileProcessEnvelopeCounter(envelope)
	if err != nil {
		return "", false, err
	}
	encodedCount, err := json.Marshal(count + 1)
	if err != nil {
		return "", false, fmt.Errorf("编码 Session 计数失败: %w", err)
	}
	envelope.Data[fileProcessCounterKey] = encodedCount
	encodedEnvelope, err := json.Marshal(envelope)
	if err != nil {
		return "", false, fmt.Errorf("编码 Session 信封失败: %w", err)
	}
	return string(encodedEnvelope), false, nil
}

// fileProcessDecodeCounter 解码由真实 File.Read 返回的 Session 信封并提取精确整数计数。
func fileProcessDecodeCounter(content string) (int, error) {
	var envelope fileProcessSessionEnvelope
	if err := json.Unmarshal([]byte(content), &envelope); err != nil {
		return 0, fmt.Errorf("解码 JSON 失败: %w", err)
	}
	if envelope.Version != fileProcessEnvelopeVersion || envelope.Data == nil {
		return 0, fmt.Errorf("Session 信封格式非法")
	}
	return fileProcessEnvelopeCounter(envelope)
}

// fileProcessEnvelopeCounter 严格解析 Session 信封中的非负整数计数。
func fileProcessEnvelopeCounter(envelope fileProcessSessionEnvelope) (int, error) {
	rawCount, found := envelope.Data[fileProcessCounterKey]
	if !found {
		return 0, nil
	}
	var count int
	if err := json.Unmarshal(rawCount, &count); err != nil || count < 0 {
		return 0, fmt.Errorf("Session 计数格式非法")
	}
	return count, nil
}
