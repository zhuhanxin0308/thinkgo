package db

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	frameworklog "github.com/zhuhanxin0308/thinkgo/v3/log"
)

const (
	databaseLogAllocationRuns = 100
	databaseLogConcurrentRuns = 64
)

type databaseLogProbeError struct {
	textCalls  atomic.Int32
	stateCalls atomic.Int32
}

func (err *databaseLogProbeError) Error() string {
	err.textCalls.Add(1)
	return "password=private-value"
}

func (err *databaseLogProbeError) SQLState() string {
	err.stateCalls.Add(1)
	return "HY000"
}

type legacyDatabaseLogCounter struct {
	errors  atomic.Int32
	queries atomic.Int32
}

func (logger *legacyDatabaseLogCounter) ErrorCtx(string, map[string]interface{}) {
	logger.errors.Add(1)
}

func (logger *legacyDatabaseLogCounter) SqlCtx(string, map[string]interface{}) {
	logger.queries.Add(1)
}

type levelAwareDatabaseLogCounter struct {
	legacyDatabaseLogCounter
	errorEnabled atomic.Bool
	sqlEnabled   atomic.Bool
}

func (logger *levelAwareDatabaseLogCounter) IsLevelEnabled(level string) bool {
	switch level {
	case frameworklog.LevelError:
		return logger.errorEnabled.Load()
	case frameworklog.LevelSQL:
		return logger.sqlEnabled.Load()
	default:
		return false
	}
}

// TestDatabaseDisabledLoggingSkipsPreparation 验证错误分类、查询形状和耗时上下文都在日志过滤后构造。
func TestDatabaseDisabledLoggingSkipsPreparation(t *testing.T) {
	filtered := frameworklog.NewLog()
	filtered.SetLevels([]string{frameworklog.LevelInfo})
	disabled := frameworklog.NewDisabledLog()
	t.Cleanup(func() { _ = filtered.Close(); _ = disabled.Close() })
	for name, logger := range map[string]Logger{
		"missing": nil, "disabled": disabled, "filtered": filtered, "optional": &levelAwareDatabaseLogCounter{},
	} {
		t.Run(name, func(t *testing.T) {
			database := NewDB(&mockConnection{})
			t.Cleanup(func() { _ = database.Close() })
			database.SetLogger(logger)
			database.queryLogging = true
			query := database.Table("accounts").WhereRaw("token = 'private-value'")
			cause := &databaseLogProbeError{}
			fields := map[string]interface{}{"arg_count": 1}
			data := map[string]interface{}{"username": "private-name", "token": "private-value"}
			for entry, report := range map[string]func() error{
				"database": func() error { return database.reportError("query", cause, fields) },
				"query":    func() error { return query.reportError("select", cause, fields) },
				"write":    func() error { return query.reportWriteError("insert", cause, data, nil) },
			} {
				t.Run(entry, func(t *testing.T) {
					allocations := testing.AllocsPerRun(databaseLogAllocationRuns, func() {
						if got := report(); got != cause {
							t.Fatal("关闭日志不能改变原始错误身份")
						}
					})
					if allocations != 0 {
						t.Errorf("关闭错误日志不应分配诊断数据，实际每次 %g 次", allocations)
					}
				})
			}
			if database.queryLoggingEnabled() {
				t.Error("SQL 级别关闭或缺少日志器时不应构造查询诊断")
			}
			allocations := testing.AllocsPerRun(databaseLogAllocationRuns, func() {
				database.traceQueryOperation("query", fields)()
				query.traceOperation("select")()
				database.reportQuery("query", time.Second, fields)
			})
			if allocations != 0 {
				t.Errorf("关闭 SQL 日志不应分配诊断数据，实际每次 %g 次", allocations)
			}
			if cause.textCalls.Load() != 0 || cause.stateCalls.Load() != 0 {
				t.Errorf("关闭日志不应读取错误诊断: Error=%d SQLState=%d", cause.textCalls.Load(), cause.stateCalls.Load())
			}
		})
	}
}

// TestDatabaseLogCapabilityAndLevelCompatibility 验证错误与 SQL 级别独立，旧接口和诊断配置继续有效。
func TestDatabaseLogCapabilityAndLevelCompatibility(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		legacy       bool
		diagnostics  bool
		errorEnabled bool
		sqlEnabled   bool
	}{
		{name: "legacy", legacy: true, diagnostics: true, errorEnabled: true, sqlEnabled: true},
		{name: "enabled", diagnostics: true, errorEnabled: true, sqlEnabled: true},
		{name: "error_only", diagnostics: true, errorEnabled: true},
		{name: "sql_only", diagnostics: true, sqlEnabled: true},
		{name: "diagnostics_off", errorEnabled: true, sqlEnabled: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			aware := &levelAwareDatabaseLogCounter{}
			aware.errorEnabled.Store(testCase.errorEnabled)
			aware.sqlEnabled.Store(testCase.sqlEnabled)
			var logger Logger = aware
			counter := &aware.legacyDatabaseLogCounter
			if testCase.legacy {
				logger = counter
			}
			database := NewDB(&mockConnection{})
			t.Cleanup(func() { _ = database.Close() })
			database.SetLogger(logger)
			database.queryLogging = testCase.diagnostics
			cause := &databaseLogProbeError{}
			if got := database.reportError("query", cause, nil); got != cause {
				t.Fatal("日志策略不能替换原始错误")
			}
			database.reportQuery("query", time.Second, nil)
			var wantErrors int32
			if testCase.errorEnabled {
				wantErrors = 1
			}
			if got := counter.errors.Load(); got != wantErrors {
				t.Fatalf("错误级别调用不符: %d", got)
			}
			wantSQL := testCase.diagnostics && testCase.sqlEnabled
			var wantQueries int32
			if wantSQL {
				wantQueries = 1
			}
			if got := counter.queries.Load(); got != wantQueries || database.queryLoggingEnabled() != wantSQL {
				t.Fatalf("SQL 能力或级别判定不符: 调用=%d", got)
			}
			if cause.textCalls.Load() != 0 {
				t.Fatal("数据库错误日志仍应只分类，不字符串化驱动错误")
			}
		})
	}
	database := NewDB(&mockConnection{})
	t.Cleanup(func() { _ = database.Close() })
	database.SetLogger(&dbTestLogger{})
	database.queryLogging = true
	if database.queryLoggingEnabled() {
		t.Fatal("只有 ErrorCtx 的旧日志器不能启用 SQL 诊断上下文")
	}
}

type disabledLoggingConnection struct {
	failingQueryConnection
	queries    atomic.Int32
	executions atomic.Int32
	streamed   atomic.Int32
}

func (connection *disabledLoggingConnection) QueryContext(context.Context, string, ...interface{}) ([]map[string]interface{}, error) {
	connection.queries.Add(1)
	return nil, connection.queryErr
}

func (connection *disabledLoggingConnection) ExecuteContext(context.Context, string, ...interface{}) (int64, error) {
	connection.executions.Add(1)
	return 0, connection.executeErr
}

func (connection *disabledLoggingConnection) SelectEach(_ context.Context, _ SelectRequest, callback func(map[string]interface{}) bool) error {
	connection.streamed.Add(1)
	callback(map[string]interface{}{"id": int64(1)})
	return nil
}

// TestDatabaseDisabledRawErrorsAddNoLoggingAllocations 验证真实原生操作失败时不再额外处理 SQL 日志内容。
func TestDatabaseDisabledRawErrorsAddNoLoggingAllocations(t *testing.T) {
	connection := &disabledLoggingConnection{}
	database := NewDB(connection)
	logger := frameworklog.NewDisabledLog()
	database.SetLogger(logger)
	database.queryLogging = true
	t.Cleanup(func() { _ = database.Close(); _ = logger.Close() })
	cause := &databaseLogProbeError{}
	ctx := context.Background()
	args := []interface{}{int64(1)}
	for _, testCase := range []struct {
		name     string
		setError func(error)
		run      func() error
	}{
		{name: "query", setError: func(err error) { connection.queryErr = err }, run: func() error {
			_, err := database.QueryContext(ctx, "SELECT 'private-value' FROM accounts WHERE id = ?", args...)
			return err
		}},
		{name: "execute", setError: func(err error) { connection.executeErr = err }, run: func() error {
			_, err := database.ExecuteContext(ctx, "UPDATE accounts SET token = 'private-value' WHERE id = ?", args...)
			return err
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			testCase.setError(nil)
			success := testing.AllocsPerRun(databaseLogAllocationRuns, func() {
				if testCase.run() != nil {
					t.Fatal("基线操作应成功")
				}
			})
			testCase.setError(cause)
			failed := testing.AllocsPerRun(databaseLogAllocationRuns, func() {
				if testCase.run() != cause {
					t.Fatal("失败操作必须保留驱动错误")
				}
			})
			if failed != success {
				t.Fatalf("关闭日志后失败路径不应增加日志分配: 成功=%g 失败=%g", success, failed)
			}
		})
	}
	if connection.queries.Load() == 0 || connection.executions.Load() == 0 {
		t.Fatal("日志关闭不能跳过真实查询或写入")
	}
	callbacks := 0
	if err := database.Table("accounts").Each(func(map[string]interface{}) bool { callbacks++; return true }); err != nil {
		t.Fatalf("关闭日志后的流式查询失败: %v", err)
	}
	if callbacks != 1 || connection.streamed.Load() != 1 {
		t.Fatal("日志关闭不能跳过流式业务回调")
	}
}

// TestDatabaseLoggerSnapshotRemainsConcurrent 验证日志器替换和级别调整不会竞态或丢失原始错误。
func TestDatabaseLoggerSnapshotRemainsConcurrent(t *testing.T) {
	logger := &levelAwareDatabaseLogCounter{}
	disabled := frameworklog.NewDisabledLog()
	database := NewDB(&mockConnection{})
	database.queryLogging = true
	t.Cleanup(func() { _ = database.Close(); _ = disabled.Close() })
	cause := &databaseLogProbeError{}
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		for range databaseLogConcurrentRuns {
			logger.errorEnabled.Store(true)
			logger.sqlEnabled.Store(true)
			database.SetLogger(logger)
			logger.errorEnabled.Store(false)
			logger.sqlEnabled.Store(false)
			database.SetLogger(disabled)
		}
	}()
	go func() {
		defer workers.Done()
		for range databaseLogConcurrentRuns {
			if database.reportError("query", cause, nil) != cause {
				t.Error("并发日志不得改变错误身份")
			}
			database.traceQueryOperation("query", nil)()
		}
	}()
	workers.Wait()
}
