package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
)

// lockSQLCapture 只记录实际发往 database/sql 的语句，锁所有权语义由现有驱动测试验证。
type lockSQLCapture struct {
	statement string
	arguments []driver.NamedValue
}

func (capture *lockSQLCapture) Connect(context.Context) (driver.Conn, error) { return capture, nil }
func (capture *lockSQLCapture) Driver() driver.Driver                        { return capture }
func (capture *lockSQLCapture) Open(string) (driver.Conn, error)             { return capture, nil }
func (capture *lockSQLCapture) Close() error                                 { return nil }
func (capture *lockSQLCapture) Begin() (driver.Tx, error) {
	return nil, errors.New("本测试不使用事务")
}
func (capture *lockSQLCapture) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (capture *lockSQLCapture) ExecContext(_ context.Context, statement string, arguments []driver.NamedValue) (driver.Result, error) {
	capture.statement = statement
	capture.arguments = append([]driver.NamedValue(nil), arguments...)
	return driver.RowsAffected(1), nil
}

// TestLockOwnerSQLUsesDialectComparison 验证续租与释放共享参数化的 LOB 比较，且保留过期和键条件。
func TestLockOwnerSQLUsesDialectComparison(t *testing.T) {
	for _, dialect := range []db.Builder{&oracleCacheBuilder{}, &builder.Pgsql{}, &builder.Mysql{}} {
		t.Run(dialect.DialectName(), func(t *testing.T) {
			capture := &lockSQLCapture{}
			pool := sql.OpenDB(capture)
			connection := &db.SQLConnection{DB: pool, Builder: dialect}
			t.Cleanup(func() { _ = connection.Close() })
			backend, err := NewDB(connection, "cache_records")
			if err != nil {
				t.Fatal(err)
			}
			const owner = "owner' OR 1=1 --"
			encodedOwner, err := dbLockOwnerValue(owner)
			if err != nil {
				t.Fatal(err)
			}
			for _, operation := range []func() (bool, error){
				func() (bool, error) { return backend.RenewLock("job", owner, time.Minute) },
				func() (bool, error) { return backend.ReleaseLock("job", owner) },
			} {
				if matched, err := operation(); err != nil || !matched {
					t.Fatalf("锁操作失败: matched=%v err=%v", matched, err)
				}
				usesLOB := strings.Contains(capture.statement, "DBMS_LOB.COMPARE(") && strings.Contains(capture.statement, "TO_CLOB(")
				if usesLOB != (dialect.DialectName() == "oracle") {
					t.Errorf("锁持有者比较不符合方言: %s", capture.statement)
				}
				if strings.Contains(capture.statement, owner) || !strings.Contains(capture.statement, "expiry") {
					t.Errorf("锁条件丢失或 owner 未绑定: %s", capture.statement)
				}
				var foundKey, foundOwner bool
				for _, argument := range capture.arguments {
					foundKey = foundKey || argument.Value == dbLockStorageKey("job")
					foundOwner = foundOwner || argument.Value == encodedOwner
				}
				if !foundKey || !foundOwner {
					t.Fatalf("缺少锁键或持有者绑定: %#v", capture.arguments)
				}
			}
		})
	}
}

type atomicSQLCapture struct {
	statements []string
	rowValue   string
}

func (c *atomicSQLCapture) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c *atomicSQLCapture) Driver() driver.Driver                        { return c }
func (c *atomicSQLCapture) Open(string) (driver.Conn, error)             { return c, nil }
func (c *atomicSQLCapture) Close() error                                 { return nil }
func (c *atomicSQLCapture) Begin() (driver.Tx, error)                    { return nil, errors.New("unused") }
func (c *atomicSQLCapture) Prepare(string) (driver.Stmt, error)          { return nil, driver.ErrSkip }
func (c *atomicSQLCapture) ExecContext(_ context.Context, stmt string, _ []driver.NamedValue) (driver.Result, error) {
	c.statements = append(c.statements, stmt)
	return driver.RowsAffected(1), nil
}
func (c *atomicSQLCapture) QueryContext(_ context.Context, _ string, args []driver.NamedValue) (driver.Rows, error) {
	if len(args) > 0 && args[0].Value == dbGenerationKey {
		return &singleValueRows{cols: []string{"key", "value", "expiry"}, row: []driver.Value{dbGenerationKey, "1", int64(0)}}, nil
	}
	return &singleValueRows{cols: []string{"key", "value", "expiry"}, row: []driver.Value{"k", c.rowValue, int64(0)}}, nil
}

type singleValueRows struct {
	cols []string
	row  []driver.Value
	done bool
}

func (r *singleValueRows) Columns() []string { return r.cols }
func (r *singleValueRows) Close() error      { return nil }
func (r *singleValueRows) Next(dest []driver.Value) error {
	if r.done || r.row == nil {
		return io.EOF
	}
	copy(dest, r.row)
	r.done = true
	return nil
}

// TestAtomicUpdateSQLUsesByteExactDialectComparison 验证 MySQL、SQL Server、Oracle 原子更新与条件删除均生成字节精确比较谓词。
func TestAtomicUpdateSQLUsesByteExactDialectComparison(t *testing.T) {
	cases := []struct {
		builder db.Builder
		expect  string
	}{
		{&builder.Mysql{}, "CAST(`value` AS BINARY) = CAST(? AS BINARY)"},
		{&builder.Sqlsrv{}, "CAST([value] AS VARBINARY(MAX)) = CAST(CAST("},
		{&oracleCacheBuilder{}, `DBMS_LOB.COMPARE("value", TO_CLOB(`},
	}
	for _, tc := range cases {
		t.Run(tc.builder.DialectName(), func(t *testing.T) {
			for _, remove := range []bool{false, true} {
				capture := &atomicSQLCapture{rowValue: `"a"`}
				pool := sql.OpenDB(capture)
				conn := &db.SQLConnection{DB: pool, Builder: tc.builder}
				t.Cleanup(func() { _ = conn.Close() })
				backend, err := NewDB(conn, "cache_records")
				if err != nil {
					t.Fatal(err)
				}
				err = backend.Update("case_key", time.Minute, func(cur interface{}, found bool) (interface{}, bool, error) {
					if !found || cur != "a" {
						t.Fatalf("unexpected existing value: found=%t cur=%#v", found, cur)
					}
					return "A", remove, nil
				})
				if err != nil {
					t.Fatalf("Update(remove=%t) failed: %v", remove, err)
				}
				matched := false
				for _, stmt := range capture.statements {
					if strings.Contains(stmt, tc.expect) {
						matched = true
						break
					}
				}
				if !matched {
					t.Fatalf("dialect %s (remove=%t) missing byte-exact predicate %q in %v", tc.builder.DialectName(), remove, tc.expect, capture.statements)
				}
			}
		})
	}
}
