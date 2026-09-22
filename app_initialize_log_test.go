package framework

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestInitializeAppliesLogRetentionConfiguration 验证应用配置会完整传递文件数量和容量治理参数。
func TestInitializeAppliesLogRetentionConfiguration(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	logDirectory := filepath.Join(basePath, "runtime", "managed-log")
	if err := os.MkdirAll(logDirectory, 0o700); err != nil {
		t.Fatalf("创建日志目录失败: %v", err)
	}
	now := time.Now()
	for offset := 1; offset <= 2; offset++ {
		filename := filepath.Join(logDirectory, now.AddDate(0, 0, -offset).Format("2006-01-02")+".log")
		if err := os.WriteFile(filename, []byte("old"), 0o600); err != nil {
			t.Fatalf("创建旧日志失败: %v", err)
		}
	}
	logConfig := fmt.Sprintf(`{
  "default": "file",
  "channels": {
    "file": {
      "type": "file",
      "path": %q,
      "max_file_size": 1048576,
      "retention_days": 30,
      "max_files": 1,
      "max_total_size": 1048576
    }
  }
}`, filepath.ToSlash(logDirectory))
	if err := os.WriteFile(filepath.Join(basePath, "config", "log.json"), []byte(logConfig), 0o600); err != nil {
		t.Fatalf("覆盖日志配置失败: %v", err)
	}

	app := mustBuildTestApp(t, basePath)
	defer func() {
		if app.log != nil {
			_ = app.log.Close()
		}
		if app.db != nil {
			_ = app.db.Close()
		}
	}()
	if err := app.StartupError(); err != nil {
		t.Fatalf("应用初始化失败: %v", err)
	}
	app.log.Info("trigger retention")
	if err := app.log.Flush(context.Background()); err != nil {
		t.Fatalf("刷盘应用日志失败: %v", err)
	}

	entries, err := os.ReadDir(logDirectory)
	if err != nil {
		t.Fatalf("读取日志目录失败: %v", err)
	}
	logCount := 0
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".log" {
			logCount++
		}
	}
	if logCount != 1 {
		t.Fatalf("max_files=1 时应只保留当前日志，实际为 %d 个", logCount)
	}
}

// TestInitializeReportsInvalidLogDirectory 验证日志目录初始化失败会进入启动错误而非延迟静默失败。
func TestInitializeReportsInvalidLogDirectory(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	invalidPath := filepath.Join(basePath, "runtime", "not-a-directory")
	if err := os.MkdirAll(filepath.Dir(invalidPath), 0o700); err != nil {
		t.Fatalf("创建运行目录失败: %v", err)
	}
	if err := os.WriteFile(invalidPath, []byte("file"), 0o600); err != nil {
		t.Fatalf("创建冲突文件失败: %v", err)
	}
	logConfig := fmt.Sprintf(`{
  "default": "file",
  "channels": {
    "file": {"type": "file", "path": %q}
  }
}`, filepath.ToSlash(invalidPath))
	if err := os.WriteFile(filepath.Join(basePath, "config", "log.json"), []byte(logConfig), 0o600); err != nil {
		t.Fatalf("覆盖日志配置失败: %v", err)
	}

	app, _ := initializeTestApp(t, basePath)
	err := app.StartupError()
	if err == nil || !strings.Contains(err.Error(), "日志目录") {
		t.Fatalf("无效日志目录应产生明确启动错误，实际为 %v", err)
	}
}

// TestReadLogFileOptionsUsesStrictIntegerSemantics 验证日志容量配置拒绝小数、字符串和 int64 溢出。
func TestReadLogFileOptionsUsesStrictIntegerSemantics(t *testing.T) {
	options, err := readLogFileOptions(map[string]interface{}{
		"max_file_size":  int64(1024),
		"retention_days": float64(7),
		"max_files":      uint16(12),
		"max_total_size": json.Number("4096"),
	})
	if err != nil {
		t.Fatalf("合法日志配置解析失败: %v", err)
	}
	if options.MaxFileSize != 1024 || options.RetentionDays != 7 || options.MaxFiles != 12 || options.MaxTotalSize != 4096 {
		t.Fatalf("日志配置解析结果错误: %#v", options)
	}

	invalidCases := []map[string]interface{}{
		{"max_file_size": 1.5},
		{"retention_days": "30"},
		{"max_files": true},
		{"max_total_size": uint64(1) << 63},
		{"max_total_size": json.Number("1.25")},
	}
	for _, invalid := range invalidCases {
		if _, err := readLogFileOptions(invalid); err == nil {
			t.Fatalf("非法日志整数配置应返回错误: %#v", invalid)
		}
	}
}

// TestCreateAppLogChannelRejectsUnsupportedAndNegativeConfiguration 验证通道构造不会对错误类型或负数配置静默回退。
func TestCreateAppLogChannelRejectsUnsupportedAndNegativeConfiguration(t *testing.T) {
	app := &App{BasePath: t.TempDir()}
	testCases := []map[string]interface{}{
		{"type": "unknown"},
		{"type": "file", "unknown": true},
		{"type": "file", "max_files": -1},
		{"type": "file", "level": []interface{}{"info", 7}},
	}
	for _, config := range testCases {
		if logger, err := createAppLogChannel(app, config, false); err == nil {
			if logger != nil {
				_ = logger.Close()
			}
			t.Fatalf("非法日志通道配置应返回错误: %#v", config)
		}
	}
}

// TestCreateAppLogChannelUsesRuntimePathForEmptyThinkPHPPath 验证 ThinkPHP
// 默认空 path 会落到 runtime/log，而不是被当作非法配置。
func TestCreateAppLogChannelUsesRuntimePathForEmptyThinkPHPPath(t *testing.T) {
	basePath := t.TempDir()
	app := &App{BasePath: basePath, RuntimePath: filepath.Join(basePath, "runtime")}
	logger, err := createAppLogChannel(app, map[string]interface{}{"type": "file", "path": ""}, false)
	if err != nil {
		t.Fatalf("ThinkPHP 默认日志路径不应失败: %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	if info, statErr := os.Stat(filepath.Join(basePath, "runtime", "log")); statErr != nil || !info.IsDir() {
		t.Fatalf("空 path 应创建 runtime/log: info=%#v err=%v", info, statErr)
	}
}

// TestCreateAppLogChannelResolvesRelativePathFromApplicationRoot 验证日志路径不依赖进程工作目录。
func TestCreateAppLogChannelResolvesRelativePathFromApplicationRoot(t *testing.T) {
	basePath := t.TempDir()
	app := &App{BasePath: basePath}
	logger, err := createAppLogChannel(app, map[string]interface{}{
		"type": "file",
		"path": filepath.Join("runtime", "relative-log"),
	}, false)
	if err != nil {
		t.Fatalf("创建相对路径日志通道失败: %v", err)
	}
	defer func() { _ = logger.Close() }()
	expected := filepath.Join(basePath, "runtime", "relative-log")
	if info, err := os.Stat(expected); err != nil || !info.IsDir() {
		t.Fatalf("相对日志目录应创建在应用根目录下，路径=%s 错误=%v", expected, err)
	}
}

// TestInitializeReportsMissingDefaultLogChannel 验证默认通道不存在时不会静默使用构造期临时日志器。
func TestInitializeReportsMissingDefaultLogChannel(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	if err := os.WriteFile(
		filepath.Join(basePath, "config", "log.json"),
		[]byte(`{"default":"missing","channels":{"file":{"type":"file"}}}`),
		0o600,
	); err != nil {
		t.Fatalf("覆盖日志配置失败: %v", err)
	}

	app, _ := initializeTestApp(t, basePath)
	err := app.StartupError()
	if err == nil || !strings.Contains(err.Error(), "默认日志通道") {
		t.Fatalf("缺失默认日志通道应产生启动错误，实际为 %v", err)
	}
}
