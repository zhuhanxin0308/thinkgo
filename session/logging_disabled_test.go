package session

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	frameworklog "github.com/zhuhanxin0308/thinkgo/v3/log"
)

const (
	sessionLogAllocationRuns = 100
	sessionLogConcurrentRuns = 64
)

type sessionLogProbeError struct {
	calls atomic.Int32
}

func (err *sessionLogProbeError) Error() string {
	err.calls.Add(1)
	return "session=" + errorKnownSessionID + " password=private-value"
}

type levelAwareSessionLogger struct {
	sessionTestLogger
	enabled atomic.Bool
}

func (logger *levelAwareSessionLogger) IsLevelEnabled(level string) bool {
	return level == frameworklog.LevelError && logger.enabled.Load()
}

// TestSessionDisabledLoggingSkipsPreparation 验证禁用和级别过滤在错误字符串化及脱敏复制之前生效。
func TestSessionDisabledLoggingSkipsPreparation(t *testing.T) {
	filtered := frameworklog.NewLog()
	filtered.SetLevels([]string{frameworklog.LevelInfo})
	disabled := frameworklog.NewDisabledLog()
	t.Cleanup(func() { _ = filtered.Close(); _ = disabled.Close() })
	for name, logger := range map[string]Logger{
		"missing": nil, "disabled": disabled, "filtered": filtered, "optional": &levelAwareSessionLogger{},
	} {
		t.Run(name, func(t *testing.T) {
			manager := &Session{logger: logger, id: errorKnownSessionID}
			cause := &sessionLogProbeError{}
			fields := map[string]interface{}{"session_id": errorKnownSessionID, "operation": "read"}
			allocations := testing.AllocsPerRun(sessionLogAllocationRuns, func() {
				if manager.reportError("读取失败", cause, fields) != cause {
					t.Fatal("日志关闭不能替换原始错误")
				}
				if manager.reportIDError("持久化失败", cause, errorKnownSessionID) != cause {
					t.Fatal("日志关闭不能替换带 Session ID 的原始错误")
				}
			})
			if allocations != 0 {
				t.Errorf("关闭错误日志不应分配日志内容，实际每次 %g 次", allocations)
			}
			if calls := cause.calls.Load(); calls != 0 {
				t.Errorf("关闭错误日志不应调用 Error，实际 %d 次", calls)
			}
			if fields["session_id"] != errorKnownSessionID {
				t.Fatal("日志过滤不得修改调用方上下文")
			}
			if manager.reportError("无错误", nil, fields) != nil {
				t.Fatal("nil 错误必须保持 nil")
			}
			if manager.reportIDError("无错误", nil, errorKnownSessionID) != nil {
				t.Fatal("带 Session ID 的 nil 错误必须保持 nil")
			}
		})
	}
}

// TestSessionEnabledAndLegacyLoggingRemainSanitized 验证可选能力不改变旧记录器和开启状态的脱敏边界。
func TestSessionEnabledAndLegacyLoggingRemainSanitized(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		aware := &levelAwareSessionLogger{}
		aware.enabled.Store(true)
		var logger Logger = aware
		if legacy {
			logger = &aware.sessionTestLogger
		}
		manager := &Session{logger: logger, id: errorKnownSessionID}
		cause := &sessionLogProbeError{}
		if manager.reportError("读取失败", cause, nil) != cause {
			t.Fatal("开启日志也必须保留原始错误")
		}
		if aware.count() != 1 || cause.calls.Load() != 1 {
			t.Fatalf("开启或旧记录器应记录一次: legacy=%t 日志=%d 文本=%d", legacy, aware.count(), cause.calls.Load())
		}
		message := aware.errorCalls[0].msg
		if strings.Contains(message, errorKnownSessionID) || strings.Contains(message, "private-value") {
			t.Fatalf("日志能力探测不能绕过既有脱敏: %s", message)
		}
	}
}

// TestSessionDisabledLoggingPreservesReadAndSaveFailure 验证真实存储错误、脏状态与成功重试不依赖日志开关。
func TestSessionDisabledLoggingPreservesReadAndSaveFailure(t *testing.T) {
	driver := newCountingDriver()
	cause := &sessionLogProbeError{}
	driver.readErr = cause
	manager := newTestSessionManager(t, driver, map[string]interface{}{"name": "SID"}, nil)
	logger := frameworklog.NewDisabledLog()
	manager.SetLogger(logger)
	t.Cleanup(func() { _ = manager.Close(); _ = logger.Close() })
	raw := httptest.NewRequest(http.MethodGet, "/", nil)
	raw.AddCookie(&http.Cookie{Name: "SID", Value: errorKnownSessionID})
	if _, err := manager.NewRequestSession(raw, httptest.NewRecorder()); err != cause {
		t.Fatal("关闭日志不能吞掉后端读取错误")
	}
	if driver.readCount != 1 {
		t.Fatal("关闭日志不能跳过后端读取")
	}
	driver.readErr = nil
	driver.updateErr = cause
	response := httptest.NewRecorder()
	request, err := manager.NewRequestSession(httptest.NewRequest(http.MethodGet, "/", nil), response)
	if err != nil {
		t.Fatalf("创建待持久化会话失败: %v", err)
	}
	if err = request.Set("uid", 1); err != nil {
		t.Fatalf("设置会话数据失败: %v", err)
	}
	if err = request.Save(); err != cause {
		t.Fatal("关闭日志不能吞掉后端持久化错误")
	}
	if !request.dirty || len(response.Header().Values("Set-Cookie")) != 0 || driver.updateCount != 1 {
		t.Fatal("持久化失败必须保留脏状态且不能下发 Cookie")
	}
	if cause.calls.Load() != 0 {
		t.Fatal("失败传播不得为关闭日志读取错误文本")
	}
	driver.updateErr = nil
	if err = request.Save(); err != nil {
		t.Fatalf("关闭日志后重试持久化失败: %v", err)
	}
	if request.dirty || driver.updateCount != 2 || len(response.Header().Values("Set-Cookie")) != 1 {
		t.Fatal("重试成功后必须持久化数据并下发 Cookie")
	}
}

// TestSessionLogSnapshotRemainsConcurrent 验证管理器日志替换与错误报告使用受保护快照。
func TestSessionLogSnapshotRemainsConcurrent(t *testing.T) {
	manager := &Session{}
	logger := &levelAwareSessionLogger{}
	disabled := frameworklog.NewDisabledLog()
	t.Cleanup(func() { _ = disabled.Close() })
	cause := &sessionLogProbeError{}
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		for range sessionLogConcurrentRuns {
			manager.SetLogger(logger)
			manager.SetLogger(disabled)
		}
	}()
	go func() {
		defer workers.Done()
		for range sessionLogConcurrentRuns {
			if manager.reportError("并发报告", cause, nil) != cause {
				t.Error("并发报告不得替换错误")
			}
		}
	}()
	workers.Wait()
	if cause.calls.Load() != 0 || logger.count() != 0 {
		t.Fatal("所有并发快照均禁用时不应处理日志")
	}
}
