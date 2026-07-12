package driver

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"thinkgo/framework/log"
)

func writeTestLogFile(t *testing.T, directory string, date time.Time, suffix string, size int) string {
	t.Helper()
	filename := filepath.Join(directory, date.Format("2006-01-02")+suffix+".log")
	content := make([]byte, size)
	for index := range content {
		content[index] = 'x'
	}
	if err := os.WriteFile(filename, content, 0o600); err != nil {
		t.Fatalf("创建测试日志文件失败: %v", err)
	}
	return filename
}

func selectedLogFile(t *testing.T, driver *File, current time.Time) string {
	t.Helper()
	filename, err := driver.getLogFile(current)
	if err != nil {
		t.Fatalf("选择日志文件失败: %v", err)
	}
	return filename
}

// currentHandle 通过反射读取 File 驱动当前缓存的文件句柄指针，便于测试句柄复用与释放。
func currentHandle(driver *File) (uintptr, bool) {
	field := reflect.ValueOf(driver).Elem().FieldByName("currentFile")
	if !field.IsValid() {
		return 0, false
	}
	if field.IsNil() {
		return 0, true
	}
	return field.Pointer(), true
}

// TestFileDriverCachesOpenFileHandle 验证同一日志文件会复用已打开的句柄，避免每条日志重复 open/close。
func TestFileDriverCachesOpenFileHandle(t *testing.T) {
	dir := t.TempDir()
	driver := NewFile(dir)
	t.Cleanup(func() {
		_ = driver.Close()
	})

	entryTime := time.Date(2026, 4, 15, 12, 0, 0, 0, time.Local)
	entry := &log.LogEntry{Time: entryTime, Level: "info", Message: "first"}
	if err := driver.WriteEntry(entry); err != nil {
		t.Fatalf("第一次写日志失败: %v", err)
	}

	before, ok := currentHandle(driver)
	if !ok {
		t.Fatal("File 驱动应维护当前文件句柄")
	}
	if before == 0 {
		t.Fatal("第一次写入后应缓存当前日志文件句柄")
	}

	secondEntry := &log.LogEntry{Time: entryTime, Level: "info", Message: "second"}
	if err := driver.WriteEntry(secondEntry); err != nil {
		t.Fatalf("第二次写日志失败: %v", err)
	}

	after, _ := currentHandle(driver)
	if after == 0 {
		t.Fatal("第二次写入后缓存句柄不应丢失")
	}
	if before != after {
		t.Fatal("同一日志文件应复用同一个已打开句柄")
	}
}

// TestFileDriverCloseReleasesCachedHandles 验证 Close 会释放缓存的文件句柄。
func TestFileDriverCloseReleasesCachedHandles(t *testing.T) {
	dir := t.TempDir()
	driver := NewFile(dir)

	entry := &log.LogEntry{
		Time:    time.Date(2026, 4, 15, 12, 0, 0, 0, time.Local),
		Level:   "info",
		Message: "close",
	}
	if err := driver.WriteEntry(entry); err != nil {
		t.Fatalf("写日志失败: %v", err)
	}

	if err := driver.Close(); err != nil {
		t.Fatalf("关闭日志驱动失败: %v", err)
	}

	handle, ok := currentHandle(driver)
	if !ok {
		t.Fatal("File 驱动应维护当前文件句柄字段")
	}
	if handle != 0 {
		t.Fatal("Close 后应释放并清空当前文件句柄")
	}
}

// TestFileDriverRotatesHandleAcrossDays 验证跨天写入会关闭旧句柄、切换到新文件，避免句柄随天数泄漏。
func TestFileDriverRotatesHandleAcrossDays(t *testing.T) {
	dir := t.TempDir()
	driver := NewFile(dir)
	t.Cleanup(func() {
		_ = driver.Close()
	})

	day1 := time.Date(2026, 4, 15, 12, 0, 0, 0, time.Local)
	if err := driver.WriteEntry(&log.LogEntry{Time: day1, Level: "info", Message: "day1"}); err != nil {
		t.Fatalf("写第一天日志失败: %v", err)
	}
	first, _ := currentHandle(driver)

	day2 := time.Date(2026, 4, 16, 12, 0, 0, 0, time.Local)
	if err := driver.WriteEntry(&log.LogEntry{Time: day2, Level: "info", Message: "day2"}); err != nil {
		t.Fatalf("写第二天日志失败: %v", err)
	}
	second, _ := currentHandle(driver)

	if second == 0 {
		t.Fatal("跨天写入后应持有新文件句柄")
	}
	if first == second {
		t.Fatal("跨天写入应切换到新文件句柄，而非复用旧句柄")
	}

	nameField := reflect.ValueOf(driver).Elem().FieldByName("currentName")
	if got := nameField.String(); got != selectedLogFile(t, driver, day2) {
		t.Fatalf("当前句柄应指向第二天日志文件，实际 %s", got)
	}
}

// TestNewFileWithOptionsRejectsInvalidConfiguration 验证无效路径和负数治理参数会被显式拒绝。
func TestNewFileWithOptionsRejectsInvalidConfiguration(t *testing.T) {
	testCases := []struct {
		name    string
		path    string
		options FileOptions
	}{
		{name: "空路径", path: "", options: FileOptions{}},
		{name: "负数文件大小", path: t.TempDir(), options: FileOptions{MaxFileSize: -1}},
		{name: "负数保留天数", path: t.TempDir(), options: FileOptions{RetentionDays: -1}},
		{name: "负数文件数量", path: t.TempDir(), options: FileOptions{MaxFiles: -1}},
		{name: "负数总容量", path: t.TempDir(), options: FileOptions{MaxTotalSize: -1}},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := NewFileWithOptions(testCase.path, testCase.options); err == nil {
				t.Fatal("无效文件日志配置应返回错误")
			}
		})
	}
}

// TestLegacyNewFileSurfacesInvalidConfigurationOnWrite 验证兼容构造器也不会把无效配置变成当前目录写入。
func TestLegacyNewFileSurfacesInvalidConfigurationOnWrite(t *testing.T) {
	driver := NewFile("", -1)
	err := driver.WriteEntry(&log.LogEntry{Time: time.Now(), Level: "info", Message: "invalid"})
	if err == nil {
		t.Fatal("兼容构造器的无效配置应在首次写入时返回错误")
	}
}

// TestFileDriverRejectsNilEntries 验证单条和批量接口不会因 nil 条目发生 panic。
func TestFileDriverRejectsNilEntries(t *testing.T) {
	driver, err := NewFileWithOptions(t.TempDir(), FileOptions{})
	if err != nil {
		t.Fatalf("创建文件日志驱动失败: %v", err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	if err := driver.WriteEntry(nil); err == nil {
		t.Fatal("WriteEntry 应拒绝 nil 条目")
	}
	if err := driver.SaveEntries([]*log.LogEntry{nil}); err == nil {
		t.Fatal("SaveEntries 应拒绝 nil 条目")
	}
}

// TestFileDriverUsesRestrictivePermissions 验证日志目录和文件不会向同机其他用户开放。
func TestFileDriverUsesRestrictivePermissions(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "private-logs")
	driver, err := NewFileWithOptions(directory, FileOptions{})
	if err != nil {
		t.Fatalf("创建文件日志驱动失败: %v", err)
	}
	t.Cleanup(func() { _ = driver.Close() })

	entryTime := time.Now()
	if err := driver.WriteEntry(&log.LogEntry{Time: entryTime, Level: "info", Message: "private"}); err != nil {
		t.Fatalf("写入私有日志失败: %v", err)
	}

	if runtime.GOOS != "windows" {
		directoryInfo, statErr := os.Stat(directory)
		if statErr != nil {
			t.Fatalf("读取日志目录权限失败: %v", statErr)
		}
		if permission := directoryInfo.Mode().Perm(); permission != 0o700 {
			t.Fatalf("日志目录权限应为 0700，实际为 %04o", permission)
		}

		fileInfo, statErr := os.Stat(selectedLogFile(t, driver, entryTime))
		if statErr != nil {
			t.Fatalf("读取日志文件权限失败: %v", statErr)
		}
		if permission := fileInfo.Mode().Perm(); permission != 0o600 {
			t.Fatalf("日志文件权限应为 0600，实际为 %04o", permission)
		}
	}
}

// TestFileDriverRetentionRemovesOnlyExpiredManagedLogs 验证按天清理仅影响框架日志文件。
func TestFileDriverRetentionRemovesOnlyExpiredManagedLogs(t *testing.T) {
	directory := t.TempDir()
	now := time.Now()
	expired := writeTestLogFile(t, directory, now.AddDate(0, 0, -10), "", 16)
	recent := writeTestLogFile(t, directory, now.AddDate(0, 0, -1), "", 16)
	unmanaged := filepath.Join(directory, "application.data")
	if err := os.WriteFile(unmanaged, []byte("keep"), 0o600); err != nil {
		t.Fatalf("创建非日志文件失败: %v", err)
	}

	driver, err := NewFileWithOptions(directory, FileOptions{
		RetentionDays: 3,
		MaxFiles:      20,
		MaxTotalSize:  1 << 20,
	})
	if err != nil {
		t.Fatalf("创建文件日志驱动失败: %v", err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	if err := driver.WriteEntry(&log.LogEntry{Time: now, Level: "info", Message: "trigger cleanup"}); err != nil {
		t.Fatalf("触发日志清理失败: %v", err)
	}

	if _, err := os.Stat(expired); !os.IsNotExist(err) {
		t.Fatalf("过期日志应被删除，实际错误为 %v", err)
	}
	for _, preserved := range []string{recent, unmanaged, selectedLogFile(t, driver, now)} {
		if _, err := os.Stat(preserved); err != nil {
			t.Fatalf("应保留文件 %s，实际错误为 %v", preserved, err)
		}
	}
}

// TestFileDriverRetentionEnforcesFileCountAndTotalSize 验证数量和总容量约束同时生效。
func TestFileDriverRetentionEnforcesFileCountAndTotalSize(t *testing.T) {
	directory := t.TempDir()
	now := time.Now()
	oldest := writeTestLogFile(t, directory, now.AddDate(0, 0, -3), "", 96)
	second := writeTestLogFile(t, directory, now.AddDate(0, 0, -2), "", 96)
	newest := writeTestLogFile(t, directory, now.AddDate(0, 0, -1), "", 32)

	driver, err := NewFileWithOptions(directory, FileOptions{
		RetentionDays: 30,
		MaxFiles:      2,
		MaxTotalSize:  160,
	})
	if err != nil {
		t.Fatalf("创建文件日志驱动失败: %v", err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	if err := driver.WriteEntry(&log.LogEntry{Time: now, Level: "info", Message: "new"}); err != nil {
		t.Fatalf("触发容量治理失败: %v", err)
	}

	for _, removed := range []string{oldest, second} {
		if _, err := os.Stat(removed); !os.IsNotExist(err) {
			t.Fatalf("最旧日志 %s 应被删除，实际错误为 %v", removed, err)
		}
	}
	for _, preserved := range []string{newest, selectedLogFile(t, driver, now)} {
		if _, err := os.Stat(preserved); err != nil {
			t.Fatalf("较新日志 %s 应保留，实际错误为 %v", preserved, err)
		}
	}
}

// TestFileDriverSaveEntriesGroupsDatesAndUsesAvailableRotation 验证批量写按日期分组并复用未满轮转文件。
func TestFileDriverSaveEntriesGroupsDatesAndUsesAvailableRotation(t *testing.T) {
	directory := t.TempDir()
	dayOne := time.Now().AddDate(0, 0, -1)
	dayTwo := time.Now()
	base := writeTestLogFile(t, directory, dayOne, "", 300)
	rotated := writeTestLogFile(t, directory, dayOne, "_1", 1)
	driver, err := NewFileWithOptions(directory, FileOptions{
		MaxFileSize:   256,
		RetentionDays: 30,
		MaxFiles:      20,
		MaxTotalSize:  1 << 20,
	})
	if err != nil {
		t.Fatalf("创建文件日志驱动失败: %v", err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	entries := []*log.LogEntry{
		{Time: dayTwo, Level: "info", Message: "day-two"},
		{Time: dayOne, Level: "warning", Message: "day-one-first"},
		{Time: dayOne, Level: "error", Message: "day-one-second"},
	}
	if err := driver.SaveEntries(entries); err != nil {
		t.Fatalf("批量写日志失败: %v", err)
	}

	baseContent, err := os.ReadFile(base)
	if err != nil {
		t.Fatalf("读取已满基础日志失败: %v", err)
	}
	if strings.Contains(string(baseContent), "day-one") {
		t.Fatalf("已满基础日志不应继续追加，实际为 %q", string(baseContent))
	}
	rotatedContent, err := os.ReadFile(rotated)
	if err != nil {
		t.Fatalf("读取轮转日志失败: %v", err)
	}
	for _, message := range []string{"day-one-first", "day-one-second"} {
		if !strings.Contains(string(rotatedContent), message) {
			t.Fatalf("轮转日志应包含 %q，实际为 %q", message, string(rotatedContent))
		}
	}
	dayTwoContent, err := os.ReadFile(filepath.Join(directory, dayTwo.Format("2006-01-02")+".log"))
	if err != nil {
		t.Fatalf("读取第二天日志失败: %v", err)
	}
	if !strings.Contains(string(dayTwoContent), "day-two") {
		t.Fatalf("第二天日志内容错误: %q", string(dayTwoContent))
	}
}

// TestFileDriverSaveEntriesRotatesWithinLargeBatch 验证单个异步批次也严格执行文件大小边界。
func TestFileDriverSaveEntriesRotatesWithinLargeBatch(t *testing.T) {
	directory := t.TempDir()
	now := time.Now()
	probe := &log.LogEntry{Time: now, Level: "info", Message: "batch-rotation-message"}
	lineSize := int64(len(probe.FormatEntry()) + 1)
	driver, err := NewFileWithOptions(directory, FileOptions{
		MaxFileSize:   lineSize + 1,
		RetentionDays: 30,
		MaxFiles:      20,
		MaxTotalSize:  1 << 20,
	})
	if err != nil {
		t.Fatalf("创建文件日志驱动失败: %v", err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	entries := []*log.LogEntry{
		{Time: now, Level: "info", Message: "batch-rotation-message"},
		{Time: now, Level: "info", Message: "batch-rotation-message"},
		{Time: now, Level: "info", Message: "batch-rotation-message"},
	}
	if err := driver.SaveEntries(entries); err != nil {
		t.Fatalf("批量写日志失败: %v", err)
	}

	files, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("读取日志目录失败: %v", err)
	}
	managedCount := 0
	for _, file := range files {
		if _, managed := parseManagedLogName(file.Name(), time.Local); !managed {
			continue
		}
		managedCount++
		content, err := os.ReadFile(filepath.Join(directory, file.Name()))
		if err != nil {
			t.Fatalf("读取轮转日志失败: %v", err)
		}
		if lines := strings.Count(string(content), "\n"); lines != 1 {
			t.Fatalf("每个轮转文件应只有一条完整日志，文件 %s 实际为 %d 条", file.Name(), lines)
		}
	}
	if managedCount != 3 {
		t.Fatalf("三条不能共存的日志应生成三个轮转文件，实际为 %d 个", managedCount)
	}
}

// TestFileDriverRotationSkipsEveryFullSegment 验证连续满文件后选择首个可用轮转编号。
func TestFileDriverRotationSkipsEveryFullSegment(t *testing.T) {
	directory := t.TempDir()
	now := time.Now()
	writeTestLogFile(t, directory, now, "", 8)
	writeTestLogFile(t, directory, now, "_1", 8)
	driver, err := NewFileWithOptions(directory, FileOptions{MaxFileSize: 8})
	if err != nil {
		t.Fatalf("创建文件日志驱动失败: %v", err)
	}
	filename, err := driver.getLogFile(now)
	if err != nil {
		t.Fatalf("选择轮转日志失败: %v", err)
	}
	expected := filepath.Join(directory, now.Format("2006-01-02")+"_2.log")
	if filename != expected {
		t.Fatalf("应选择第二个轮转文件 %s，实际为 %s", expected, filename)
	}
}

// TestParseManagedLogNameStrictlyMatchesOwnedFiles 验证清理器不会把相似文件名误判为框架日志。
func TestParseManagedLogNameStrictlyMatchesOwnedFiles(t *testing.T) {
	location := time.Local
	testCases := []struct {
		name    string
		managed bool
	}{
		{name: "2026-07-11.log", managed: true},
		{name: "2026-07-11_1.log", managed: true},
		{name: "2026-07-11_25.log", managed: true},
		{name: "2026-07-11.txt", managed: false},
		{name: "short.log", managed: false},
		{name: "2026-99-11.log", managed: false},
		{name: "2026-07-11-extra.log", managed: false},
		{name: "2026-07-11_0.log", managed: false},
		{name: "2026-07-11_bad.log", managed: false},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, managed := parseManagedLogName(testCase.name, location)
			if managed != testCase.managed {
				t.Fatalf("文件名 %s 的识别结果应为 %v，实际为 %v", testCase.name, testCase.managed, managed)
			}
		})
	}
}

// TestFileDriverReportsUnresolvableCapacityLimit 验证当前文件单独超限时返回可观测错误且不删除当前文件。
func TestFileDriverReportsUnresolvableCapacityLimit(t *testing.T) {
	directory := t.TempDir()
	driver, err := NewFileWithOptions(directory, FileOptions{
		MaxFileSize:   1024,
		RetentionDays: 30,
		MaxFiles:      1,
		MaxTotalSize:  1,
	})
	if err != nil {
		t.Fatalf("创建文件日志驱动失败: %v", err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	now := time.Now()
	err = driver.WriteEntry(&log.LogEntry{Time: now, Level: "info", Message: "larger than one byte"})
	if err == nil || !strings.Contains(err.Error(), "无可清理文件") {
		t.Fatalf("无法满足总容量限制时应返回明确错误，实际为 %v", err)
	}
	if _, statErr := os.Stat(selectedLogFile(t, driver, now)); statErr != nil {
		t.Fatalf("容量超限时不得删除当前打开日志，实际错误为 %v", statErr)
	}
}

// TestNewFileWithOptionsReportsDirectoryConflict 验证日志路径被普通文件占用时构造立即失败。
func TestNewFileWithOptionsReportsDirectoryConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "occupied")
	if err := os.WriteFile(path, []byte("occupied"), 0o600); err != nil {
		t.Fatalf("创建冲突文件失败: %v", err)
	}
	if _, err := NewFileWithOptions(path, FileOptions{}); err == nil {
		t.Fatal("日志目录被文件占用时应返回错误")
	}
}
