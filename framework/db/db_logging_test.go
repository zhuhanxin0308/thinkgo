package db

import (
	"errors"
	"strings"
	"testing"
)

type failingQueryConnection struct {
	selectErr  error
	queryErr   error
	executeErr error
}

func (c *failingQueryConnection) Select(table string, fields string, where []string, args []interface{}, order string, limit int, offset int) ([]map[string]interface{}, error) {
	return nil, c.selectErr
}

func (c *failingQueryConnection) Insert(table string, data map[string]interface{}) (int64, error) {
	return 0, c.executeErr
}

func (c *failingQueryConnection) Update(table string, data map[string]interface{}, where []string, args []interface{}) (int64, error) {
	return 0, c.executeErr
}

func (c *failingQueryConnection) Delete(table string, where []string, args []interface{}) (int64, error) {
	return 0, c.executeErr
}

func (c *failingQueryConnection) Count(table string, where []string, args []interface{}) (int64, error) {
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
