package db

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type queryDiagnosticConnector struct {
	connection Connection
}

func (connector *queryDiagnosticConnector) Connect(Config) (Connection, error) {
	return connector.connection, nil
}

type queryDiagnosticLogger struct {
	lock     sync.Mutex
	sqlCalls []dbLogCall
}

func (logger *queryDiagnosticLogger) ErrorCtx(string, map[string]interface{}) {}

func (logger *queryDiagnosticLogger) SqlCtx(message string, context map[string]interface{}) {
	logger.lock.Lock()
	defer logger.lock.Unlock()
	cloned := make(map[string]interface{}, len(context))
	for key, value := range context {
		cloned[key] = value
	}
	logger.sqlCalls = append(logger.sqlCalls, dbLogCall{msg: message, ctx: cloned})
}

func (logger *queryDiagnosticLogger) calls() []dbLogCall {
	logger.lock.Lock()
	defer logger.lock.Unlock()
	return append([]dbLogCall(nil), logger.sqlCalls...)
}

// TestQueryDiagnosticsAreExplicitAndRedacted 验证诊断关闭时不记录，开启后也不泄露绑定值或 SQL 字面量。
func TestQueryDiagnosticsAreExplicitAndRedacted(t *testing.T) {
	const secret = "customer-secret-9f1a"
	connection := &modelBusinessConnection{rows: []map[string]interface{}{{"value": int64(1)}}}
	database := NewDB(connection)
	logger := &queryDiagnosticLogger{}
	database.SetLogger(logger)

	if _, err := database.Query("SELECT ? AS value", secret); err != nil {
		t.Fatalf("关闭诊断时查询失败: %v", err)
	}
	if calls := logger.calls(); len(calls) != 0 {
		t.Fatalf("未启用诊断时不应记录 SQL，实际为 %#v", calls)
	}

	database.queryLogging = true
	if _, err := database.Query("SELECT '"+secret+"' AS literal, ? AS value", secret); err != nil {
		t.Fatalf("开启诊断时查询失败: %v", err)
	}
	calls := logger.calls()
	if len(calls) != 1 {
		t.Fatalf("开启诊断后应记录一次查询，实际为 %d", len(calls))
	}
	dumped := fmt.Sprintf("%s %#v", calls[0].msg, calls[0].ctx)
	if strings.Contains(dumped, secret) {
		t.Fatalf("SQL 诊断泄露了敏感值: %s", dumped)
	}
	if calls[0].ctx["operation"] != "query" || calls[0].ctx["arg_count"] != 1 {
		t.Fatalf("SQL 诊断上下文错误: %#v", calls[0].ctx)
	}
	if _, exists := calls[0].ctx["duration_us"]; !exists {
		t.Fatalf("SQL 诊断缺少耗时: %#v", calls[0].ctx)
	}
}

// TestORMDiagnosticsContainSafeQueryShape 验证 ORM 诊断只记录查询形状而不记录绑定值。
func TestORMDiagnosticsContainSafeQueryShape(t *testing.T) {
	const secret = "orm-password-41d2"
	database := NewDB(&mockConnection{})
	database.queryLogging = true
	logger := &queryDiagnosticLogger{}
	database.SetLogger(logger)

	if _, err := database.Table("users").Where("password = ?", secret).Select(); err != nil {
		t.Fatalf("ORM 查询失败: %v", err)
	}
	calls := logger.calls()
	if len(calls) != 1 {
		t.Fatalf("ORM 查询应记录一次诊断，实际为 %d", len(calls))
	}
	dumped := fmt.Sprintf("%#v", calls[0].ctx)
	if strings.Contains(dumped, secret) {
		t.Fatalf("ORM 诊断泄露了绑定值: %s", dumped)
	}
	if calls[0].ctx["table"] != "users" || calls[0].ctx["arg_count"] != 1 {
		t.Fatalf("ORM 诊断查询形状错误: %#v", calls[0].ctx)
	}
}

// TestConnectEnablesDiagnosticsFromEitherFlag 验证两个公开配置开关都能驱动数据库诊断。
func TestConnectEnablesDiagnosticsFromEitherFlag(t *testing.T) {
	testCases := []struct {
		name       string
		debug      bool
		triggerSQL bool
	}{
		{name: "debug", debug: true},
		{name: "trigger_sql", triggerSQL: true},
	}
	for index, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			connectorName := fmt.Sprintf("query_diagnostic_%d_%d", time.Now().UnixNano(), index)
			if err := RegisterConnector(connectorName, &queryDiagnosticConnector{connection: &mockConnection{}}); err != nil {
				t.Fatalf("注册诊断测试连接器失败: %v", err)
			}
			database, err := Connect(Config{
				Type:       connectorName,
				Debug:      testCase.debug,
				TriggerSQL: testCase.triggerSQL,
			})
			if err != nil {
				t.Fatalf("连接诊断测试数据库失败: %v", err)
			}
			t.Cleanup(func() { _ = database.Close() })
			if !database.queryLogging {
				t.Fatalf("%s 未启用数据库诊断", testCase.name)
			}
		})
	}
}
