package framework

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
	"github.com/zhuhanxin0308/thinkgo/v3/env"
	"github.com/zhuhanxin0308/thinkgo/v3/log"
	"github.com/zhuhanxin0308/thinkgo/v3/view"
)

type filteredControllerViewDriver struct {
	controllerDebugViewDriver
	failure error
	renders int
}

func (driver *filteredControllerViewDriver) Display(io.Writer, string, map[string]interface{}) error {
	driver.renders++
	return driver.failure
}

// TestControllerViewFiltersErrorLogging 验证日志过滤不会跳过渲染或改变安全的错误页面。
func TestControllerViewFiltersErrorLogging(t *testing.T) {
	for _, mode := range []string{"disabled", "filtered", "enabled"} {
		t.Run(mode, func(t *testing.T) {
			capture := &databaseWarningCaptureDriver{}
			logger := newCallsiteTestLogger(t, mode, capture)
			failure := &lifecycleLogCountingError{}
			driver := &filteredControllerViewDriver{failure: failure}
			manager := view.NewView(nil, nil)
			if err := manager.SetDriver(driver); err != nil {
				t.Fatalf("安装错误视图驱动失败: %v", err)
			}
			controller := &Controller{App: &App{view: manager, log: logger}}
			if body := controller.View("private-template"); body != "页面渲染出错，请稍后再试" {
				t.Fatalf("日志配置改变了安全的视图错误响应: %q", body)
			}
			if driver.renders != 1 {
				t.Fatalf("日志过滤不得跳过视图驱动，实际渲染 %d 次", driver.renders)
			}
			wantCalls := 0
			if mode == "enabled" {
				wantCalls = 1
			}
			if failure.calls != wantCalls {
				t.Errorf("视图日志错误求值次数为 %d，期望 %d", failure.calls, wantCalls)
			}
			if err := logger.Flush(context.Background()); err != nil {
				t.Fatalf("刷新视图日志失败: %v", err)
			}
			if logged := strings.Contains(capture.messages(), "模板渲染失败 [private-template]"); logged != (mode == "enabled") {
				t.Fatalf("视图错误记录与级别不一致: %q", capture.messages())
			}
		})
	}
}

// Windows 等平台的时钟精度不能保证时间戳唯一，包级序号覆盖子用例与重复运行。
var filteredWarningConnectorSequence atomic.Uint64

type filteredWarningDatabaseConnector struct {
	failure error
}

func (connector *filteredWarningDatabaseConnector) Connect(db.Config) (db.Connection, error) {
	return nil, connector.failure
}

// TestDatabaseConnectionFiltersLoggingOnly 验证关闭日志保留 readiness、required 启动错误与重试行为。
func TestDatabaseConnectionFiltersLoggingOnly(t *testing.T) {
	for _, policy := range []DatabaseStartupPolicy{DatabaseStartupDegraded, DatabaseStartupRequired} {
		for _, mode := range []string{"disabled", "filtered", "enabled"} {
			t.Run(string(policy)+"/"+mode, func(t *testing.T) {
				capture := &databaseWarningCaptureDriver{}
				logger := newCallsiteTestLogger(t, mode, capture)
				failure := &lifecycleLogCountingError{}
				connectorName := fmt.Sprintf("filtered_warning_%d", filteredWarningConnectorSequence.Add(1))
				if err := db.RegisterConnector(connectorName, &filteredWarningDatabaseConnector{failure: failure}); err != nil {
					t.Fatalf("注册连接失败测试驱动失败: %v", err)
				}
				manager := db.NewManager("primary")
				t.Cleanup(func() { _ = manager.Close() })
				app := &App{dbManager: manager, env: env.NewEnv(), log: logger, databaseStartupPolicy: policy}
				app.initDatabaseConnections(map[string]interface{}{
					"connections": map[string]interface{}{
						"primary": map[string]interface{}{"type": connectorName, "database": "test"},
					},
				}, "primary")
				if app.databaseReadinessError() != failure {
					t.Fatal("关闭日志不得丢失 readiness 的原始失败")
				}
				wantCalls := 0
				if mode == "enabled" {
					wantCalls++
				}
				if policy == DatabaseStartupRequired {
					// required 的业务错误包装本身需要错误文本，不能被日志开关跳过。
					wantCalls++
					if !errors.Is(app.StartupError(), failure) {
						t.Fatal("关闭日志不得移除 required 策略的启动失败")
					}
				} else if app.StartupError() != nil {
					t.Fatal("degraded 策略不应因日志配置而阻断启动")
				}
				if failure.calls != wantCalls {
					t.Errorf("连接初始化错误求值次数为 %d，期望 %d", failure.calls, wantCalls)
				}
				if policy == DatabaseStartupDegraded {
					failure.calls = 0
					if connection, err := manager.Default(); connection != nil || !errors.Is(err, failure) {
						t.Fatal("关闭日志不得移除惰性重试或改变原始错误链")
					}
					// Manager 返回给调用方的错误包装必须继续执行。
					if failure.calls != wantCalls+1 {
						t.Errorf("重试错误求值次数为 %d，期望 %d", failure.calls, wantCalls+1)
					}
				}
				if err := logger.Flush(context.Background()); err != nil {
					t.Fatalf("刷新连接警告失败: %v", err)
				}
				if logged := strings.Contains(capture.messages(), `数据库连接 "primary" 失败`); logged != (mode == "enabled") {
					t.Fatalf("连接警告与级别不一致: %q", capture.messages())
				}
			})
		}
	}
}

func newCallsiteTestLogger(t *testing.T, mode string, capture *databaseWarningCaptureDriver) *log.Log {
	t.Helper()
	var logger *log.Log
	if mode == "disabled" {
		logger = log.NewDisabledLog()
	} else {
		logger = log.NewLog(capture)
		if mode == "filtered" {
			logger.SetLevels([]string{log.LevelInfo})
		}
	}
	t.Cleanup(func() { _ = logger.Close() })
	return logger
}
