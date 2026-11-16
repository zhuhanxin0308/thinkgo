//go:build oracle

package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
)

type oracleReturningTestDriver struct {
	*pinnedSQLDriver
	columnType string
	returned   any
	lookups    int
	inserts    int
}

func (recording *oracleReturningTestDriver) Open(name string) (driver.Conn, error) {
	connection, err := recording.pinnedSQLDriver.Open(name)
	if err != nil {
		return nil, err
	}
	return &oracleReturningTestConn{pinnedSQLDriverConnection: connection.(*pinnedSQLDriverConnection), recording: recording}, nil
}

type oracleReturningTestConn struct {
	*pinnedSQLDriverConnection
	recording *oracleReturningTestDriver
	resolved  bool
}

func (*oracleReturningTestConn) CheckNamedValue(*driver.NamedValue) error { return nil }

func (connection *oracleReturningTestConn) ResetSession(context.Context) error {
	connection.resolved = false
	return nil
}

func (connection *oracleReturningTestConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if !strings.Contains(strings.ToLower(query), "all_tab_columns") || len(args) != 3 || args[0].Value != "APP" || args[1].Value != "ACCOUNTS" || args[2].Value != "ACCOUNT_ID" {
		return nil, fmt.Errorf("列类型查询未按模式、表和主键绑定: %s %#v", query, args)
	}
	connection.resolved = true
	connection.recording.lookups++
	return &hardeningSQLRows{columns: []string{"data_type"}, values: []driver.Value{connection.recording.columnType}}, nil
}

func (connection *oracleReturningTestConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	connection.recording.inserts++
	if connection.recording.columnType != "" && !connection.resolved {
		return nil, errors.New("同一 Oracle 会话上未先解析主键类型")
	}
	if !strings.Contains(query, `RETURNING "ACCOUNT_ID" INTO :`) || len(args) == 0 {
		return nil, fmt.Errorf("未使用 Oracle RETURNING: %s", query)
	}
	output, ok := args[len(args)-1].Value.(sql.Out)
	if !ok {
		return nil, errors.New("未绑定 Oracle sql.Out")
	}
	switch target := output.Dest.(type) {
	case *string:
		value, ok := connection.recording.returned.(string)
		if !ok {
			return nil, fmt.Errorf("字符输出错误: %T", connection.recording.returned)
		}
		*target = value
	case *int64:
		value, ok := connection.recording.returned.(int64)
		if !ok {
			return nil, fmt.Errorf("整数输出错误: %T", connection.recording.returned)
		}
		*target = value
	default:
		return nil, fmt.Errorf("不支持的输出绑定: %T", output.Dest)
	}
	return driver.RowsAffected(1), nil
}

func newOracleReturningTestDB(t *testing.T, columnType string, returned any) (*DB, *oracleReturningTestDriver) {
	t.Helper()
	recording := &oracleReturningTestDriver{
		pinnedSQLDriver: &pinnedSQLDriver{state: &pinnedSQLDriverState{}},
		columnType:      columnType,
		returned:        returned,
	}
	name := fmt.Sprintf("thinkgo_oracle_return_%d", pinnedSQLDriverSequence.Add(1))
	sql.Register(name, recording)
	handle, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	handle.SetMaxOpenConns(1)
	database := NewDB(&SQLConnection{DB: handle, Builder: &builder.Oracle{}})
	t.Cleanup(func() { _ = database.Close() })
	return database, recording
}

func TestOracleReturningUsesTypedKeyOnSameSession(t *testing.T) {
	for _, test := range []struct {
		name, columnType string
		explicit, want   any
	}{
		{name: "显式字符串", explicit: "00042", want: "00042"},
		{name: "生成字符串", columnType: "VARCHAR2", want: "001-订单"},
		{name: "生成整数", columnType: "NUMBER", want: int64(42)},
	} {
		for _, transactional := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/事务=%t", test.name, transactional), func(t *testing.T) {
				database, recording := newOracleReturningTestDB(t, test.columnType, test.want)
				ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
				defer cancel()
				data := map[string]any{"name": "Ada"}
				if test.explicit != nil {
					data["account_id"] = test.explicit
				}
				insert := func(query *Query) error {
					query.insertPrimaryKey = "account_id"
					actual, err := query.WithContext(ctx).InsertGetId(data)
					if err == nil && actual != test.want {
						return fmt.Errorf("回传值错误: %#v", actual)
					}
					return err
				}
				var err error
				if transactional {
					err = database.TransactionContext(ctx, func(tx *Tx) error { return insert(tx.Table("app.accounts")) })
				} else {
					err = insert(database.Table("app.accounts"))
				}
				lookups := 0
				if test.columnType != "" {
					lookups = 1
				}
				if err != nil || recording.lookups != lookups || recording.inserts != 1 {
					t.Fatalf("主键回传或执行顺序错误: %v，查询 %d 次，写入 %d 次", err, recording.lookups, recording.inserts)
				}
			})
		}
	}
}

func TestOracleReturningRejectsUnsupportedColumnBeforeInsert(t *testing.T) {
	database, recording := newOracleReturningTestDB(t, "RAW", nil)
	id, err := NewModel(database, "app.accounts").PrimaryKey("account_id").InsertGetId(map[string]any{"name": "Ada"})
	if id != nil || !errors.Is(err, ErrInsertIDUnavailable) || errors.Is(err, ErrPartialWrite) || recording.lookups != 1 || recording.inserts != 0 {
		t.Fatalf("未知类型必须在写入前失败: %v %v，查询 %d 次，写入 %d 次", id, err, recording.lookups, recording.inserts)
	}
}

func TestOracleReturningPopulatesGeneratedModelKey(t *testing.T) {
	database, recording := newOracleReturningTestDB(t, "NVARCHAR2", "000-created")
	var record = struct {
		AccountID string `thinkgo:"account_id"`
		Name      string
	}{Name: "Ada"}
	if err := NewModel(database, "app.accounts").PrimaryKey("account_id").Create(&record); err != nil ||
		record.AccountID != "000-created" || recording.lookups != 1 || recording.inserts != 1 {
		t.Fatalf("模型应获得真实字符串主键: %#v，%v，查询 %d 次，写入 %d 次", record, err, recording.lookups, recording.inserts)
	}
}

func TestOracleReturningReportsPartialWriteWhenStringOutputIsMissing(t *testing.T) {
	database, recording := newOracleReturningTestDB(t, "", "")
	id, err := NewModel(database, "app.accounts").PrimaryKey("account_id").InsertGetId(map[string]any{"account_id": "account", "name": "Ada"})
	requireInsertIDPartial(t, err, ErrInsertIDUnavailable)
	if id != nil || recording.inserts != 1 || recording.lookups != 0 {
		t.Fatalf("写入后的缺失主键应继续保留写入结果: %v，查询 %d 次，写入 %d 次", id, recording.lookups, recording.inserts)
	}
}
