package db

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
)

var hardeningSQLDriverSequence atomic.Uint64

type hardeningSQLRecorder struct {
	mu         sync.Mutex
	query      string
	execCount  int
	beginCount int
}

func (r *hardeningSQLRecorder) recordedQuery() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.query
}

func (r *hardeningSQLRecorder) recordedExecCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.execCount
}

func (r *hardeningSQLRecorder) recordedBeginCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.beginCount
}

type hardeningSQLDriver struct{ recorder *hardeningSQLRecorder }

func (d *hardeningSQLDriver) Open(string) (driver.Conn, error) {
	return &hardeningSQLDriverConnection{recorder: d.recorder}, nil
}

type hardeningSQLDriverConnection struct{ recorder *hardeningSQLRecorder }

func (*hardeningSQLDriverConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("测试驱动不支持预编译语句")
}
func (*hardeningSQLDriverConnection) Close() error { return nil }
func (c *hardeningSQLDriverConnection) Begin() (driver.Tx, error) {
	c.recorder.mu.Lock()
	c.recorder.beginCount++
	c.recorder.mu.Unlock()
	return &hardeningSQLDriverTransaction{}, nil
}
func (c *hardeningSQLDriverConnection) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	c.recorder.mu.Lock()
	c.recorder.beginCount++
	c.recorder.mu.Unlock()
	return &hardeningSQLDriverTransaction{}, nil
}
func (c *hardeningSQLDriverConnection) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if c.recorder != nil {
		c.recorder.mu.Lock()
		c.recorder.query = query
		c.recorder.mu.Unlock()
	}
	if strings.Contains(query, "duplicated") {
		return &hardeningSQLRows{columns: []string{"duplicated", "duplicated"}, values: []driver.Value{int64(1), int64(2)}}, nil
	}
	if strings.Contains(query, "multiple") {
		return &hardeningSQLRows{
			columns: []string{"id", "payload"},
			rows: [][]driver.Value{
				{int64(1), []byte{0x00, 0xff}},
				{int64(2), []byte{0x01, 0xfe}},
			},
		}, nil
	}
	if strings.Contains(query, "payload") {
		if strings.Contains(query, "reused_payloads") {
			return &reusingBinaryRows{values: [][]byte{[]byte("first"), []byte("second")}, buffer: make([]byte, 6)}, nil
		}
		return &hardeningSQLRows{columns: []string{"payload"}, values: []driver.Value{[]byte{0x00, 0xff}}}, nil
	}
	return &hardeningSQLRows{columns: []string{"value"}, values: []driver.Value{int64(1)}}, nil
}

func (c *hardeningSQLDriverConnection) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if c.recorder != nil {
		c.recorder.mu.Lock()
		c.recorder.query = query
		c.recorder.execCount++
		c.recorder.mu.Unlock()
	}
	return hardeningSQLResult{lastInsertID: 7, rowsAffected: 2}, nil
}

type hardeningSQLResult struct {
	lastInsertID int64
	rowsAffected int64
}

func (result hardeningSQLResult) LastInsertId() (int64, error) { return result.lastInsertID, nil }
func (result hardeningSQLResult) RowsAffected() (int64, error) { return result.rowsAffected, nil }

type hardeningSQLDriverTransaction struct{}

func (*hardeningSQLDriverTransaction) Commit() error   { return nil }
func (*hardeningSQLDriverTransaction) Rollback() error { return nil }

type hardeningSQLRows struct {
	columns []string
	values  []driver.Value
	rows    [][]driver.Value
	read    bool
	index   int
}

type reusingBinaryRows struct {
	values [][]byte
	buffer []byte
	index  int
}

func (*reusingBinaryRows) Columns() []string { return []string{"payload"} }
func (*reusingBinaryRows) Close() error      { return nil }
func (r *reusingBinaryRows) Next(destination []driver.Value) error {
	if r.index >= len(r.values) {
		return io.EOF
	}
	value := r.values[r.index]
	copy(r.buffer, value)
	destination[0] = r.buffer[:len(value)]
	r.index++
	return nil
}

func (r *hardeningSQLRows) Columns() []string { return r.columns }
func (*hardeningSQLRows) Close() error        { return nil }
func (r *hardeningSQLRows) Next(destination []driver.Value) error {
	if r.rows != nil {
		if r.index >= len(r.rows) {
			return io.EOF
		}
		copy(destination, r.rows[r.index])
		r.index++
		return nil
	}
	if r.read {
		return io.EOF
	}
	copy(destination, r.values)
	r.read = true
	return nil
}

func newHardeningSQLConnection(t *testing.T) *SQLConnection {
	connection, _ := newRecordingHardeningSQLConnection(t)
	return connection
}

func newRecordingHardeningSQLConnection(t *testing.T) (*SQLConnection, *hardeningSQLRecorder) {
	t.Helper()
	recorder := &hardeningSQLRecorder{}
	driverName := fmt.Sprintf("thinkgo_sql_hardening_%d", hardeningSQLDriverSequence.Add(1))
	sql.Register(driverName, &hardeningSQLDriver{recorder: recorder})
	handle, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if err := handle.Close(); err != nil {
			t.Errorf("关闭测试数据库失败: %v", err)
		}
	})
	return &SQLConnection{DB: handle, Builder: &builder.Sqlite{}}, recorder
}

// TestSQLConnectionValidatesDependenciesAndContext 验证无效依赖不会演变为空指针崩溃。
func TestSQLConnectionValidatesDependenciesAndContext(t *testing.T) {
	invalid := &SQLConnection{}
	if _, err := invalid.Query("SELECT 1"); !errors.Is(err, ErrDatabaseUnavailable) {
		t.Fatalf("缺失 DB/Builder 应返回 ErrDatabaseUnavailable，实际为 %v", err)
	}
	connection := newHardeningSQLConnection(t)
	// 类型化空上下文用于验证驱动边界，不代表生产调用方式。
	var nilContext context.Context
	if _, err := connection.QueryContext(nilContext, "SELECT 1"); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("nil context 应返回 ErrInvalidQuery，实际为 %v", err)
	}
	if _, err := connection.Query("   "); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("空原生 SQL 应返回 ErrInvalidQuery，实际为 %v", err)
	}
}

// TestSQLConnectionPreservesBinaryColumns 验证 BLOB 保持字节语义且不被强制转成文本。
func TestSQLConnectionPreservesBinaryColumns(t *testing.T) {
	connection := newHardeningSQLConnection(t)
	rows, err := connection.Query("SELECT CAST(X'00FF' AS BLOB) AS payload")
	if err != nil {
		t.Fatalf("查询 BLOB 失败: %v", err)
	}
	payload, ok := rows[0]["payload"].([]byte)
	if !ok || !bytes.Equal(payload, []byte{0x00, 0xff}) {
		t.Fatalf("BLOB 类型或内容被破坏: %#v", rows[0]["payload"])
	}
}

// TestSQLConnectionPreservesMultipleRows 验证复用扫描缓冲时仍保留每行的独立结果和二进制类型。
func TestSQLConnectionPreservesMultipleRows(t *testing.T) {
	connection := newHardeningSQLConnection(t)
	rows, err := connection.Query("SELECT id, payload FROM multiple")
	if err != nil {
		t.Fatalf("多行查询失败: %v", err)
	}
	if len(rows) != 2 || rows[0]["id"] != int64(1) || rows[1]["id"] != int64(2) {
		t.Fatalf("多行结果错误: %#v", rows)
	}
	first, ok := rows[0]["payload"].([]byte)
	if !ok || !bytes.Equal(first, []byte{0x00, 0xff}) {
		t.Fatalf("第一行二进制结果错误: %#v", rows[0]["payload"])
	}
	second, ok := rows[1]["payload"].([]byte)
	if !ok || !bytes.Equal(second, []byte{0x01, 0xfe}) {
		t.Fatalf("第二行二进制结果错误: %#v", rows[1]["payload"])
	}
}

func TestScanRowsDoesNotShareReusedBuffers(t *testing.T) {
	connection := newHardeningSQLConnection(t)
	rows, err := connection.Query("SELECT payload FROM reused_payloads")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || string(rows[0]["payload"].([]byte)) != "first" || string(rows[1]["payload"].([]byte)) != "second" {
		t.Fatalf("unexpected binary rows: %#v", rows)
	}
	rows[0]["payload"].([]byte)[0] = 'X'
	if string(rows[1]["payload"].([]byte)) != "second" {
		t.Fatalf("result rows share a driver buffer: %#v", rows)
	}
}

// TestSQLConnectionSupportsPostgreSQLJSONBQuestionOperators 验证原生 SQL 从参数计数到
// PostgreSQL 重绑定的完整路径都能区分 JSONB 操作符与绑定占位符。
func TestSQLConnectionSupportsPostgreSQLJSONBQuestionOperators(t *testing.T) {
	connection, recorder := newRecordingHardeningSQLConnection(t)
	connection.Builder = &builder.Pgsql{}
	query := "SELECT payload FROM documents WHERE payload ?? ? AND tags ?| ? AND payload @? ?"
	rows, err := connection.Query(query, "role", "{role,name}", "$.role")
	if err != nil || len(rows) != 1 {
		t.Fatalf("PostgreSQL JSONB 原生查询失败: rows=%#v err=%v", rows, err)
	}
	want := "SELECT payload FROM documents WHERE payload ? $1 AND tags ?| $2 AND payload @? $3"
	if got := recorder.recordedQuery(); got != want {
		t.Fatalf("PostgreSQL JSONB 最终 SQL 错误:\n got %q\nwant %q", got, want)
	}
}

// TestSQLConnectionRejectsDuplicateColumnNames 验证行映射不会静默覆盖同名列。
func TestSQLConnectionRejectsDuplicateColumnNames(t *testing.T) {
	connection := newHardeningSQLConnection(t)
	_, err := connection.Query("SELECT 1 AS duplicated, 2 AS duplicated")
	if !errors.Is(err, ErrInvalidDatabaseRow) {
		t.Fatalf("同名列应返回 ErrInvalidDatabaseRow，实际为 %v", err)
	}
}

// TestSQLConnectionCRUDAndRawExecution 验证公开的同步 CRUD 包装器、计数和原生执行
// 都经过方言构建器并返回驱动提供的真实结果。
func TestSQLConnectionCRUDAndRawExecution(t *testing.T) {
	connection, recorder := newRecordingHardeningSQLConnection(t)
	rows, err := connection.Select(context.Background(), newSelectRequest(
		"users", "value", mustTestPredicate(t, []string{"id = ?"}, []interface{}{1}), "id", "id DESC", 1, 0, nil,
	))
	if err != nil || len(rows) != 1 || rows[0]["value"] != int64(1) {
		t.Fatalf("SQL Select 结果错误: rows=%#v err=%v", rows, err)
	}
	if !strings.Contains(recorder.recordedQuery(), `SELECT "value" FROM "users" WHERE id = ? ORDER BY "id" DESC LIMIT 1`) {
		t.Fatalf("SQL Select 构造错误: %q", recorder.recordedQuery())
	}
	if result, err := connection.Insert(context.Background(), newInsertRequest("users", map[string]interface{}{"name": "Ada"}, "id", true)); err != nil || result.ID != int64(7) {
		id, _ := result.ID.(int64)
		t.Fatalf("SQL Insert 结果错误: id=%d err=%v", id, err)
	}
	if result, err := connection.Update(context.Background(), newUpdateRequest("users", map[string]interface{}{"name": "Grace"}, mustTestPredicate(t, []string{"id = ?"}, []interface{}{1}), "id")); err != nil || result.Count() != 2 {
		affected := result.Count()
		t.Fatalf("SQL Update 结果错误: affected=%d err=%v", affected, err)
	}
	if result, err := connection.Delete(context.Background(), newDeleteRequest("users", mustTestPredicate(t, []string{"id = ?"}, []interface{}{1}), "id", false)); err != nil || result.Deleted != 2 {
		affected := result.Deleted
		t.Fatalf("SQL Delete 结果错误: affected=%d err=%v", affected, err)
	}
	if count, err := connection.Count(context.Background(), newCountRequest("users", mustTestPredicate(t, []string{"active = ?"}, []interface{}{true}), "id")); err != nil || count != 1 {
		t.Fatalf("SQL Count 结果错误: count=%d err=%v", count, err)
	}
	if affected, err := connection.Execute("UPDATE users SET active = ? WHERE id = ?", true, 1); err != nil || affected != 2 {
		t.Fatalf("SQL Execute 结果错误: affected=%d err=%v", affected, err)
	}
}

func TestSQLConnectionExecutesTypedOperations(t *testing.T) {
	connection, recorder := newRecordingHardeningSQLConnection(t)
	predicate := newPredicate().appendValidated("id = ?", []interface{}{7})

	updated, err := connection.Update(context.Background(), newUpdateRequest(
		"users",
		map[string]interface{}{"name": "Ada"},
		predicate,
		"id",
	))
	if err != nil || updated.Affected != 2 || updated.ModifiedKnown || updated.MatchedKnown {
		t.Fatalf("typed update 结果错误: %#v, %v", updated, err)
	}
	if got := recorder.recordedQuery(); !strings.Contains(got, `UPDATE "users" SET "name" = ? WHERE id = ?`) {
		t.Fatalf("typed update SQL 错误: %q", got)
	}

	deleted, err := connection.Delete(context.Background(), newDeleteRequest("users", predicate, "id", false))
	if err != nil || deleted.Deleted != 2 {
		t.Fatalf("typed delete 结果错误: %#v, %v", deleted, err)
	}
}

func TestSQLConnectionSeparatesInsertCountAndID(t *testing.T) {
	connection := newHardeningSQLConnection(t)
	countResult, err := connection.Insert(context.Background(), newInsertRequest(
		"users",
		map[string]interface{}{"name": "Ada"},
		"id",
		false,
	))
	if err != nil || countResult.Affected != 2 || countResult.IDKnown {
		t.Fatalf("普通 Insert 应只返回数量: %#v, %v", countResult, err)
	}

	idResult, err := connection.Insert(context.Background(), newInsertRequest(
		"users",
		map[string]interface{}{"name": "Grace"},
		"id",
		true,
	))
	if err != nil || idResult.Affected != 2 || !idResult.IDKnown || idResult.ID != int64(7) {
		t.Fatalf("InsertGetId 路径应同时保留数量和真实 ID: %#v, %v", idResult, err)
	}
}

func TestSQLConnectionCompilesTypedAggregate(t *testing.T) {
	connection, recorder := newRecordingHardeningSQLConnection(t)
	request := newSelectRequest(
		"orders",
		"*",
		newPredicate(),
		"id",
		"",
		0,
		0,
		&AggregateExpression{Function: "SUM", Field: "amount", Alias: "tp_aggregate"},
	)
	if _, err := connection.Select(context.Background(), request); err != nil {
		t.Fatalf("typed aggregate 不应被普通字段校验拒绝: %v", err)
	}
	if got := recorder.recordedQuery(); got != `SELECT SUM("amount") AS "tp_aggregate" FROM "orders"` {
		t.Fatalf("typed aggregate SQL 错误: %q", got)
	}
}

// TestSQLConnectionRejectsInvalidStructuredArguments 验证低层 SQL 连接在访问驱动前
// 拒绝非法标识符、分页、空写入、危险全表操作和占位符错配。
func TestSQLConnectionRejectsInvalidStructuredArguments(t *testing.T) {
	connection := newHardeningSQLConnection(t)
	selectCases := []struct {
		name   string
		table  string
		fields string
		where  []string
		args   []interface{}
		order  string
		limit  int
		offset int
		want   error
	}{
		{name: "非法表名", table: "bad table", fields: "*", want: ErrInvalidQuery},
		{name: "非法字段", table: "users", fields: "name;drop", want: ErrInvalidQuery},
		{name: "非法排序", table: "users", fields: "*", order: "id sideways", want: ErrInvalidQuery},
		{name: "负数分页", table: "users", fields: "*", limit: -1, want: ErrInvalidPagination},
		{name: "占位符错配", table: "users", fields: "*", where: []string{"id = ?"}, want: ErrInvalidQuery},
	}
	for _, testCase := range selectCases {
		t.Run(testCase.name, func(t *testing.T) {
			predicate := newPredicate()
			for _, clause := range testCase.where {
				predicate = predicate.appendValidated(clause, testCase.args)
			}
			_, err := connection.Select(context.Background(), newSelectRequest(testCase.table, testCase.fields, predicate, "id", testCase.order, testCase.limit, testCase.offset, nil))
			if !errors.Is(err, testCase.want) {
				t.Fatalf("应返回 %v，实际为 %v", testCase.want, err)
			}
		})
	}

	if _, err := connection.Insert(context.Background(), newInsertRequest("users", nil, "id", false)); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("空写入应返回 ErrInvalidQuery，实际为 %v", err)
	}
	if _, err := connection.Insert(context.Background(), newInsertRequest("users", map[string]interface{}{"name": "Ada"}, "bad key", true)); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("非法回传主键应返回 ErrInvalidQuery，实际为 %v", err)
	}
	if _, err := connection.Update(context.Background(), newUpdateRequest("users", map[string]interface{}{"name": "Ada"}, newPredicate(), "id")); !errors.Is(err, ErrUnsafeFullTableMutation) {
		t.Fatalf("无条件更新应返回 ErrUnsafeFullTableMutation，实际为 %v", err)
	}
	if _, err := connection.Delete(context.Background(), newDeleteRequest("users", newPredicate(), "id", false)); !errors.Is(err, ErrUnsafeFullTableMutation) {
		t.Fatalf("无条件删除应返回 ErrUnsafeFullTableMutation，实际为 %v", err)
	}
	if _, err := connection.Delete(context.Background(), newDeleteRequest("bad table", mustTestPredicate(t, []string{"id = ?"}, []interface{}{1}), "id", false)); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("非法删除表应返回 ErrInvalidQuery，实际为 %v", err)
	}
	if _, err := connection.Count(context.Background(), newCountRequest("users", newPredicate().appendValidated("id = ?", nil), "id")); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("计数占位符错配应返回 ErrInvalidQuery，实际为 %v", err)
	}
	if _, err := connection.Execute("UPDATE users SET active = ?", true, false); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("原生执行占位符错配应返回 ErrInvalidQuery，实际为 %v", err)
	}
	if err := (&SQLConnection{}).Close(); !errors.Is(err, ErrDatabaseUnavailable) {
		t.Fatalf("关闭无效连接应返回 ErrDatabaseUnavailable，实际为 %v", err)
	}
}

// TestSQLConnectionCloseReleasesHandle 验证直接关闭连接会释放 database/sql 句柄。
func TestSQLConnectionCloseReleasesHandle(t *testing.T) {
	connection := newHardeningSQLConnection(t)
	if err := connection.Close(); err != nil {
		t.Fatalf("关闭 SQL 连接失败: %v", err)
	}
	if _, err := connection.Query("SELECT 1"); !errors.Is(err, sql.ErrConnDone) && err == nil {
		t.Fatal("关闭后的 SQL 连接不得继续成功查询")
	}
}
