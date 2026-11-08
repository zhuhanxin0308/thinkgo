package log

import (
	"context"
	"errors"
	"sync"
	"testing"
)

const (
	disabledLogAllocationRuns = 100
	disabledLogConcurrentRuns = 64
	disabledLogConcurrentJobs = 4
)

// TestDisabledLogSkipsEveryRecordingEntryPoint 验证禁用发生在格式化、上下文复制和队列创建之前。
func TestDisabledLogSkipsEveryRecordingEntryPoint(t *testing.T) {
	logger := NewDisabledLog()
	t.Cleanup(func() { closeTestLogger(t, logger) })
	driver := newMockDriver()
	if err := logger.AddDriver(driver); err != nil {
		t.Fatalf("添加禁用日志器拥有的驱动失败: %v", err)
	}
	logger.SetCallerEnabled(true)
	fields := map[string]interface{}{
		"request": map[string]interface{}{"path": "/account", "token": "private-token"},
	}
	value := &countingStringer{}
	formatArgs := []interface{}{value}
	ctx := context.Background()
	cases := []struct {
		name string
		run  func() error
	}{
		{name: "Record", run: func() error { logger.Record("消息", "custom"); return nil }},
		{name: "Write", run: func() error { logger.Write("消息", LevelInfo); return nil }},
		{name: "Emergency", run: func() error { logger.Emergency("消息"); return nil }},
		{name: "Alert", run: func() error { logger.Alert("消息"); return nil }},
		{name: "Critical", run: func() error { logger.Critical("消息"); return nil }},
		{name: "Error", run: func() error { logger.Error("消息"); return nil }},
		{name: "Warning", run: func() error { logger.Warning("消息"); return nil }},
		{name: "Notice", run: func() error { logger.Notice("消息"); return nil }},
		{name: "Info", run: func() error { logger.Info("消息"); return nil }},
		{name: "Debug", run: func() error { logger.Debug("消息"); return nil }},
		{name: "Sql", run: func() error { logger.Sql("消息"); return nil }},
		{name: "ErrorCtx", run: func() error { logger.ErrorCtx("消息", fields); return nil }},
		{name: "WarningCtx", run: func() error { logger.WarningCtx("消息", fields); return nil }},
		{name: "InfoCtx", run: func() error { logger.InfoCtx("消息", fields); return nil }},
		{name: "DebugCtx", run: func() error { logger.DebugCtx("消息", fields); return nil }},
		{name: "SqlCtx", run: func() error { logger.SqlCtx("消息", fields); return nil }},
		{name: "Errorf", run: func() error { logger.Errorf("值=%s", formatArgs...); return nil }},
		{name: "Warningf", run: func() error { logger.Warningf("值=%s", formatArgs...); return nil }},
		{name: "Infof", run: func() error { logger.Infof("值=%s", formatArgs...); return nil }},
		{name: "Debugf", run: func() error { logger.Debugf("值=%s", formatArgs...); return nil }},
		{name: "Sqlf", run: func() error { logger.Sqlf("值=%s", formatArgs...); return nil }},
		{name: "RecordContext", run: func() error { return logger.RecordContext(ctx, "消息", LevelInfo, fields) }},
		{name: "RecordContextNonBlocking", run: func() error {
			return logger.RecordContextNonBlocking(ctx, "消息", LevelInfo, fields)
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			allocations := testing.AllocsPerRun(disabledLogAllocationRuns, func() {
				if err := testCase.run(); err != nil {
					t.Fatalf("禁用日志应成功忽略记录，实际为 %v", err)
				}
			})
			if allocations != 0 {
				t.Fatalf("禁用入口不应分配日志数据，实际每次分配 %g 次", allocations)
			}
			assertDisabledLogIdle(t, logger)
		})
	}
	if err := logger.Flush(ctx); err != nil {
		t.Fatalf("刷新禁用日志器失败: %v", err)
	}
	assertDisabledLogIdle(t, logger)
	if calls := value.calls.Load(); calls != 0 {
		t.Fatalf("禁用日志不应调用 String，实际调用 %d 次", calls)
	}
	driver.lock.Lock()
	saved, written := len(driver.saved), len(driver.written)
	driver.lock.Unlock()
	if saved != 0 || written != 0 {
		t.Fatalf("禁用日志不应访问驱动，实际保存批次=%d 同步写入=%d", saved, written)
	}
}

// TestDisabledLogCannotBeEnabledByLevelConfiguration 验证独立禁用状态不被空级别或显式级别覆盖。
func TestDisabledLogCannotBeEnabledByLevelConfiguration(t *testing.T) {
	logger := NewDisabledLog()
	t.Cleanup(func() { closeTestLogger(t, logger) })
	levels := []string{
		LevelEmergency, LevelAlert, LevelCritical, LevelError, LevelWarning,
		LevelNotice, LevelInfo, LevelDebug, LevelSQL, "custom", "", " ERROR ",
	}
	for _, configured := range [][]string{nil, {}, levels} {
		logger.SetLevels(configured)
		for _, level := range levels {
			if logger.IsLevelEnabled(level) {
				t.Fatalf("显式禁用不能被级别配置重新启用: 配置=%v 查询=%q", configured, level)
			}
		}
	}
	if logger.Channel("missing") != logger || logger.Channel("") != logger {
		t.Fatal("未注册通道必须回退到当前禁用实例")
	}
	logger.ensureAsyncStarted()
	assertDisabledLogIdle(t, logger)
}

// TestDisabledLogContextValidationAndFiltering 验证参数校验保留，配置过滤不计为上下文拒绝。
func TestDisabledLogContextValidationAndFiltering(t *testing.T) {
	logger := NewDisabledLog()
	t.Cleanup(func() { closeTestLogger(t, logger) })
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for name, record := range map[string]func(context.Context, string, string, map[string]interface{}) error{
		"RecordContext":            logger.RecordContext,
		"RecordContextNonBlocking": logger.RecordContextNonBlocking,
	} {
		t.Run(name, func(t *testing.T) {
			if err := record(nil, "消息", LevelInfo, nil); !errors.Is(err, ErrInvalidLogContext) {
				t.Fatalf("nil 上下文必须返回参数错误，实际为 %v", err)
			}
			if err := record(canceled, "消息", LevelInfo, nil); err != nil {
				t.Fatalf("被配置过滤的日志不应因上下文取消而报错: %v", err)
			}
		})
	}
	assertDisabledLogIdle(t, logger)
}

// TestDisabledLogFlushPreservesCloseContract 验证禁用不等于关闭，刷新不创建队列且关闭错误优先。
func TestDisabledLogFlushPreservesCloseContract(t *testing.T) {
	logger := NewDisabledLog()
	t.Cleanup(func() { closeTestLogger(t, logger) })
	if err := logger.CloseContext(nil); !errors.Is(err, ErrInvalidLogCloseContext) {
		t.Fatalf("nil 关闭上下文必须返回参数错误，实际为 %v", err)
	}
	for _, ctx := range []context.Context{nil, context.Background()} {
		if err := logger.Flush(ctx); err != nil {
			t.Fatalf("未关闭的禁用实例应允许刷新，实际为 %v", err)
		}
	}
	assertDisabledLogIdle(t, logger)
	if err := logger.Close(); err != nil {
		t.Fatalf("关闭禁用日志器失败: %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{nil, context.Background(), canceled} {
		if err := logger.Flush(ctx); !errors.Is(err, ErrLogClosed) {
			t.Fatalf("关闭后的刷新必须返回 ErrLogClosed，实际为 %v", err)
		}
	}
	if err := logger.AddDriver(newMockDriver()); !errors.Is(err, ErrLogClosed) {
		t.Fatalf("关闭后的禁用实例必须拒绝添加驱动，实际为 %v", err)
	}
	assertDisabledLogIdle(t, logger)
}

// TestDisabledLogPreservesChannelOwnershipAndClose 验证禁用实例仍拥有资源，子通道独立过滤并可靠排空。
func TestDisabledLogPreservesChannelOwnershipAndClose(t *testing.T) {
	rootErr := errors.New("父日志驱动关闭失败")
	childErr := errors.New("子日志驱动关闭失败")
	rootDriver := &countingCloseDriver{closeErr: rootErr}
	childDriver := &countingCloseDriver{closeErr: childErr}
	recorded := newMockDriver()
	root := NewDisabledLog()
	child := NewLog(recorded, childDriver)
	other := NewDisabledLog()
	t.Cleanup(func() {
		_ = root.Close()
		_ = child.Close()
		closeTestLogger(t, other)
	})
	if err := root.AddDriver(rootDriver); err != nil {
		t.Fatalf("登记禁用实例驱动失败: %v", err)
	}
	if err := root.RegisterChannel("audit", child); err != nil {
		t.Fatalf("注册独立子通道失败: %v", err)
	}
	if err := other.RegisterChannel("audit", child); !errors.Is(err, ErrLogChannelOwned) {
		t.Fatalf("禁用实例不能绕过子通道所有权，实际为 %v", err)
	}
	if root.IsLevelEnabled(LevelInfo) || !root.Channel("audit").IsLevelEnabled(LevelInfo) {
		t.Fatal("父实例禁用状态不应改写独立子通道的级别配置")
	}
	root.Channel("audit").Info("子通道消息")
	for range 2 {
		if err := root.Close(); !errors.Is(err, rootErr) || !errors.Is(err, childErr) {
			t.Fatalf("禁用日志器关闭必须聚合并保留资源错误，实际为 %v", err)
		}
	}
	if rootDriver.closeCount() != 1 || childDriver.closeCount() != 1 {
		t.Fatalf("驱动只能关闭一次，实际父驱动=%d 子驱动=%d", rootDriver.closeCount(), childDriver.closeCount())
	}
	if recorded.totalSavedCount() != 1 {
		t.Fatalf("父实例关闭必须排空独立子通道，实际保存 %d 条", recorded.totalSavedCount())
	}
	assertDisabledLogIdle(t, root)
}

// TestDriverlessAndZeroLogRemainEnabled 验证显式禁用不改变无驱动、零值和空级别集合的兼容行为。
func TestDriverlessAndZeroLogRemainEnabled(t *testing.T) {
	for name, logger := range map[string]*Log{"constructor": NewLog(), "zero": {}} {
		t.Run(name, func(t *testing.T) {
			t.Cleanup(func() { closeTestLogger(t, logger) })
			for _, levels := range [][]string{nil, {}} {
				logger.SetLevels(levels)
				if !logger.IsLevelEnabled("custom") {
					t.Fatal("无驱动或零值实例的空级别集合必须放行自定义级别")
				}
			}
			ctx := context.Background()
			if err := logger.RecordContext(ctx, "无驱动记录", "custom", nil); err != nil {
				t.Fatalf("无驱动实例仍应接受记录，实际为 %v", err)
			}
			if err := logger.Flush(ctx); err != nil {
				t.Fatalf("无驱动实例仍应允许刷新，实际为 %v", err)
			}
			driver := newMockDriver()
			if err := logger.AddDriver(driver); err != nil {
				t.Fatalf("无驱动实例仍应允许后续添加驱动，实际为 %v", err)
			}
			logger.Record("异步记录", "custom")
			logger.Write("同步记录", "custom")
			if err := logger.Flush(ctx); err != nil {
				t.Fatalf("刷新新驱动失败: %v", err)
			}
			driver.lock.Lock()
			written := len(driver.written)
			driver.lock.Unlock()
			if driver.totalSavedCount() != 1 || written != 1 {
				t.Fatalf("添加驱动后应正常记录，实际异步=%d 同步=%d", driver.totalSavedCount(), written)
			}
		})
	}
}

// TestDisabledLogConcurrentConfigurationAndClose 验证级别快照更新、记录、刷新与关闭并发时仍无后台队列。
func TestDisabledLogConcurrentConfigurationAndClose(t *testing.T) {
	logger := NewDisabledLog()
	t.Cleanup(func() { closeTestLogger(t, logger) })
	start := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(disabledLogConcurrentJobs + 1)
	for range disabledLogConcurrentJobs {
		go func() {
			defer workers.Done()
			<-start
			for range disabledLogConcurrentRuns {
				logger.SetLevels([]string{LevelInfo})
				logger.Info("并发记录")
				logger.SetLevels(nil)
				if logger.IsLevelEnabled(LevelInfo) {
					t.Error("并发配置不能重新启用禁用实例")
				}
				if err := logger.RecordContext(context.Background(), "并发记录", LevelInfo, nil); err != nil {
					t.Errorf("被配置过滤的记录不应失败: %v", err)
				}
				if err := logger.Flush(nil); err != nil && !errors.Is(err, ErrLogClosed) {
					t.Errorf("并发刷新返回意外错误: %v", err)
				}
			}
		}()
	}
	go func() {
		defer workers.Done()
		<-start
		if err := logger.Close(); err != nil {
			t.Errorf("并发关闭禁用日志器失败: %v", err)
		}
	}()
	close(start)
	workers.Wait()
	assertDisabledLogIdle(t, logger)
}

// assertDisabledLogIdle 检查无记录资源和无拒绝计数，不依赖后台调度时机。
func assertDisabledLogIdle(t *testing.T, logger *Log) {
	t.Helper()
	logger.stateMu.RLock()
	started := logger.asyncCh != nil || logger.flushCh != nil || logger.asyncDone != nil
	logger.stateMu.RUnlock()
	if started {
		t.Fatal("显式禁用实例不应创建异步日志资源")
	}
	if dropped := logger.DroppedEntryCount(); dropped != 0 {
		t.Fatalf("配置过滤不应记为队列丢弃，实际为 %d", dropped)
	}
	if failures := logger.DriverErrorCount(); failures != 0 {
		// 资源关闭失败保留原有可观测错误，调用方在对应测试中单独验证。
		logger.stateMu.RLock()
		closed := logger.closed
		logger.stateMu.RUnlock()
		if !closed {
			t.Fatalf("禁用记录不应触发驱动错误，实际为 %d", failures)
		}
	}
}
