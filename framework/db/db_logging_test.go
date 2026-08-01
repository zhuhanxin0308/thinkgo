package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type failingQueryConnection struct {
	connectionIdentityState
	selectErr  error
	queryErr   error
	executeErr error
}

func (c *failingQueryConnection) Select(context.Context, SelectRequest) ([]map[string]interface{}, error) {
	return nil, c.selectErr
}

func (c *failingQueryConnection) Insert(context.Context, InsertRequest) (InsertResult, error) {
	return InsertResult{}, c.executeErr
}

func (c *failingQueryConnection) Update(context.Context, UpdateRequest) (UpdateResult, error) {
	return UpdateResult{}, c.executeErr
}

func (c *failingQueryConnection) Delete(context.Context, DeleteRequest) (DeleteResult, error) {
	return DeleteResult{}, c.executeErr
}

func (c *failingQueryConnection) Count(context.Context, CountRequest) (int64, error) {
	return 0, c.executeErr
}

func (c *failingQueryConnection) Close() error {
	return nil
}

func (c *failingQueryConnection) Query(sql string, args ...interface{}) ([]map[string]interface{}, error) {
	return nil, c.queryErr
}

func (c *failingQueryConnection) Execute(sql string, args ...interface{}) (int64, error) {
	return 0, c.executeErr
}

type dbLogCall struct {
	msg string
	ctx map[string]interface{}
}

type dbTestLogger struct {
	errorCalls []dbLogCall
}

func (l *dbTestLogger) ErrorCtx(msg string, ctx map[string]interface{}) {
	l.errorCalls = append(l.errorCalls, dbLogCall{msg: msg, ctx: ctx})
}

func TestDriverErrorTextIsReturnedButNotLogged(t *testing.T) {
	const secret = "customer-token-7f3a"
	driverErr := fmt.Errorf("duplicate value %s", secret)
	logger := &dbTestLogger{}
	database := NewDB(&failingQueryConnection{selectErr: driverErr})
	database.SetLogger(logger)

	_, err := database.Table("users").Select()
	if !errors.Is(err, driverErr) {
		t.Fatalf("returned error lost the original driver error: %v", err)
	}
	if len(logger.errorCalls) != 1 {
		t.Fatalf("expected one error log, got %d", len(logger.errorCalls))
	}
	logged := fmt.Sprintf("%s %#v", logger.errorCalls[0].msg, logger.errorCalls[0].ctx)
	if strings.Contains(logged, secret) {
		t.Fatalf("log leaked driver error text: %s", logged)
	}
	if !strings.Contains(logged, "error_type") {
		t.Fatalf("log is missing safe error classification: %s", logged)
	}
}

// TestQueryWriteLogsDatabaseErrors 验证插入、更新、删除失败时，
// 数据库层会统一写入错误日志，避免写操作异常被静默吞掉。
func TestQueryWriteLogsDatabaseErrors(t *testing.T) {
	testCases := []struct {
		name      string
		operation string
		execute   func(database *DB) error
	}{
		{
			name:      "insert",
			operation: "insert",
			execute: func(database *DB) error {
				_, err := database.Table("users").Insert(map[string]interface{}{"username": "tester"})
				return err
			},
		},
		{
			name:      "update",
			operation: "update",
			execute: func(database *DB) error {
				_, err := database.Table("users").Where("id = ?", 1).Update(map[string]interface{}{"username": "tester"})
				return err
			},
		},
		{
			name:      "delete",
			operation: "delete",
			execute: func(database *DB) error {
				_, err := database.Table("users").Where("id = ?", 1).Delete()
				return err
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			logger := &dbTestLogger{}
			database := NewDB(&failingQueryConnection{executeErr: errors.New(testCase.name + " failed")})
			database.SetLogger(logger)

			err := testCase.execute(database)
			if err == nil {
				t.Fatalf("%s 失败时应返回错误", testCase.operation)
			}
			if len(logger.errorCalls) != 1 {
				t.Fatalf("%s 失败应记录 1 条错误日志，实际为 %d", testCase.operation, len(logger.errorCalls))
			}
			if logger.errorCalls[0].ctx["operation"] != testCase.operation {
				t.Fatalf("日志上下文中的 operation 应为 %s，实际为 %#v", testCase.operation, logger.errorCalls[0].ctx["operation"])
			}
			if logger.errorCalls[0].ctx["table"] != "users" {
				t.Fatalf("日志上下文中的 table 应为 users，实际为 %#v", logger.errorCalls[0].ctx["table"])
			}
		})
	}
}

// TestQuerySelectLogsDatabaseErrors 验证查询失败会被统一写入日志，避免数据库层静默失联。
func TestQuerySelectLogsDatabaseErrors(t *testing.T) {
	logger := &dbTestLogger{}
	database := NewDB(&failingQueryConnection{selectErr: errors.New("select failed")})
	database.SetLogger(logger)

	_, err := database.Table("users").Where("id = ?", 1).Select()
	if err == nil {
		t.Fatal("查询失败时应返回错误")
	}
	if len(logger.errorCalls) != 1 {
		t.Fatalf("查询失败应记录 1 条错误日志，实际为 %d", len(logger.errorCalls))
	}
	if logger.errorCalls[0].ctx["operation"] != "select" {
		t.Fatalf("日志上下文中的 operation 应为 select，实际为 %#v", logger.errorCalls[0].ctx["operation"])
	}
	if logger.errorCalls[0].ctx["table"] != "users" {
		t.Fatalf("日志上下文中的 table 应为 users，实际为 %#v", logger.errorCalls[0].ctx["table"])
	}
}

// TestDBQueryLogsRawSQLErrors 验证原生 SQL 失败同样会进入统一错误日志。
func TestDBQueryLogsRawSQLErrors(t *testing.T) {
	logger := &dbTestLogger{}
	database := NewDB(&failingQueryConnection{queryErr: errors.New("query failed")})
	database.SetLogger(logger)

	_, err := database.Query("SELECT * FROM users WHERE id = ?", 7)
	if err == nil {
		t.Fatal("原生 SQL 失败时应返回错误")
	}
	if len(logger.errorCalls) != 1 {
		t.Fatalf("原生 SQL 失败应记录 1 条错误日志，实际为 %d", len(logger.errorCalls))
	}
	rawSQL, _ := logger.errorCalls[0].ctx["sql"].(string)
	if !strings.Contains(rawSQL, "SELECT * FROM users") {
		t.Fatalf("日志上下文中应保留原始 SQL，实际为 %#v", logger.errorCalls[0].ctx["sql"])
	}
}
