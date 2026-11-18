package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
)

// batch4bSQLQuery 记录单条底层 SQL 执行与参数快照。
type batch4bSQLQuery struct {
	query string
	args  []driver.Value
}

type batch4bSQLState struct {
	mu            sync.Mutex
	queries       []batch4bSQLQuery
	execAffected  int64
	lastInsertID  int64
	lastInsertErr error
	idReads       atomic.Int64
	queryHandler  func(query string, args []driver.Value) ([]string, [][]driver.Value, error)
	execHandler   func(query string, args []driver.Value) (int64, error)
}

func (s *batch4bSQLState) recordedQueries() []batch4bSQLQuery {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]batch4bSQLQuery, len(s.queries))
	copy(out, s.queries)
	return out
}

type batch4bConnector struct{ state *batch4bSQLState }

func (c batch4bConnector) Connect(context.Context) (driver.Conn, error) {
	return &batch4bConn{state: c.state}, nil
}

func (c batch4bConnector) Driver() driver.Driver { return batch4bDriver(c) }

type batch4bDriver struct{ state *batch4bSQLState }

func (d batch4bDriver) Open(string) (driver.Conn, error) {
	return &batch4bConn{state: d.state}, nil
}

type batch4bConn struct{ state *batch4bSQLState }

func (*batch4bConn) Close() error { return nil }

func (c *batch4bConn) Prepare(query string) (driver.Stmt, error) {
	return &batch4bStmt{conn: c, query: query}, nil
}

func (c *batch4bConn) Begin() (driver.Tx, error) {
	return &batch4bTx{}, nil
}

func (c *batch4bConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return &batch4bTx{}, nil
}

func namedValuesToValues(named []driver.NamedValue) []driver.Value {
	values := make([]driver.Value, len(named))
	for i, nv := range named {
		values[i] = nv.Value
	}
	return values
}

func (c *batch4bConn) ExecContext(ctx context.Context, query string, named []driver.NamedValue) (driver.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	args := namedValuesToValues(named)
	c.state.mu.Lock()
	c.state.queries = append(c.state.queries, batch4bSQLQuery{query: query, args: args})
	affected := c.state.execAffected
	handler := c.state.execHandler
	c.state.mu.Unlock()
	if handler != nil {
		var err error
		affected, err = handler(query, args)
		if err != nil {
			return nil, err
		}
	}
	return &batch4bResult{state: c.state, affected: affected}, nil
}

func (c *batch4bConn) QueryContext(ctx context.Context, query string, named []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	args := namedValuesToValues(named)
	c.state.mu.Lock()
	c.state.queries = append(c.state.queries, batch4bSQLQuery{query: query, args: args})
	handler := c.state.queryHandler
	c.state.mu.Unlock()
	if handler != nil {
		cols, rows, err := handler(query, args)
		if err != nil {
			return nil, err
		}
		return &batch4bRows{columns: cols, rows: rows}, nil
	}
	return &batch4bRows{columns: []string{"id"}, rows: nil}, nil
}

type batch4bStmt struct {
	conn  *batch4bConn
	query string
}

func (*batch4bStmt) Close() error  { return nil }
func (*batch4bStmt) NumInput() int { return -1 }

func (s *batch4bStmt) Exec(args []driver.Value) (driver.Result, error) {
	named := make([]driver.NamedValue, len(args))
	for i, v := range args {
		named[i] = driver.NamedValue{Ordinal: i + 1, Value: v}
	}
	return s.conn.ExecContext(context.Background(), s.query, named)
}

func (s *batch4bStmt) Query(args []driver.Value) (driver.Rows, error) {
	named := make([]driver.NamedValue, len(args))
	for i, v := range args {
		named[i] = driver.NamedValue{Ordinal: i + 1, Value: v}
	}
	return s.conn.QueryContext(context.Background(), s.query, named)
}

type batch4bTx struct{}

func (*batch4bTx) Commit() error   { return nil }
func (*batch4bTx) Rollback() error { return nil }

type batch4bResult struct {
	state    *batch4bSQLState
	affected int64
}

func (r *batch4bResult) LastInsertId() (int64, error) {
	r.state.idReads.Add(1)
	return r.state.lastInsertID, r.state.lastInsertErr
}

func (r *batch4bResult) RowsAffected() (int64, error) {
	return r.affected, nil
}

type batch4bRows struct {
	columns []string
	rows    [][]driver.Value
	index   int
}

func (r *batch4bRows) Columns() []string { return r.columns }
func (*batch4bRows) Close() error        { return nil }
func (r *batch4bRows) Next(dest []driver.Value) error {
	if r.index >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.index])
	r.index++
	return nil
}

func newBatch4bSQLDatabase(t *testing.T, dialect Builder, state *batch4bSQLState) *DB {
	t.Helper()
	if state.execAffected == 0 {
		state.execAffected = 1
	}
	raw := sql.OpenDB(batch4bConnector{state: state})
	raw.SetMaxOpenConns(2)
	database := NewDB(NewSQLConnection(raw, dialect))
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("关闭测试数据库失败: %v", err)
		}
	})
	return database
}

type noGenPKConnection struct {
	capturingConnection
}

func (*noGenPKConnection) GeneratesStoragePrimaryKey(string) bool { return false }

// ============================================================
// Issue #594 回归测试
// ============================================================

type issue594StringUUIDModel struct {
	*Model
	ID   string `thinkgo:"id"`
	Name string `thinkgo:"name"`
}

type issue594OmitEmptyStringModel struct {
	ID   string `thinkgo:"id,omitempty"`
	Name string `thinkgo:"name"`
}

type issue594SnowflakeOrderModel struct {
	*Model
	ID   int64  `thinkgo:"id"`
	Name string `thinkgo:"name"`
}

func TestIssue594_BeforeInsertAndSetterBusinessPrimaryKeys(t *testing.T) {
	for _, dialect := range []Builder{&builder.Sqlite{}, &builder.Mysql{}} {
		t.Run(dialect.DialectName()+"_before_insert_string_uuid", func(t *testing.T) {
			state := &batch4bSQLState{
				lastInsertErr: errors.New("非自增 UUID 表不应读取 LastInsertId"),
			}
			database := newBatch4bSQLDatabase(t, dialect, state)

			mdl := NewModel(database, "uuid_users")
			if err := mdl.On(ModelBeforeInsert, func(d map[string]any) bool {
				d["id"] = "550e8400-e29b-41d4-a716-446655440000"
				return true
			}); err != nil {
				t.Fatal(err)
			}

			rec := &issue594StringUUIDModel{Name: "Alice"}
			if err := mdl.Create(rec); err != nil {
				t.Fatalf("before_insert 生成 UUID 应插入成功: %v", err)
			}
			if rec.ID != "550e8400-e29b-41d4-a716-446655440000" {
				t.Fatalf("UUID 主键未回填: got=%q", rec.ID)
			}
			if state.idReads.Load() != 0 {
				t.Fatalf("显式业务主键不应读取 LastInsertId: reads=%d", state.idReads.Load())
			}

			// Save(unboundZeroStruct) 同样走创建分支并回填钩子生成的 UUID
			rec2 := &issue594StringUUIDModel{Name: "Bob"}
			if err := mdl.Save(rec2); err != nil || rec2.ID != "550e8400-e29b-41d4-a716-446655440000" {
				t.Fatalf("Save 零值字符串主键应通过 before_insert 创建并回填: id=%q err=%v", rec2.ID, err)
			}

			// NewModelFor 绑定记录同样支持 before_insert 生成字符串主键
			rec3 := &issue594StringUUIDModel{Name: "Carol"}
			boundMdl, err := NewModelFor(context.Background(), database, rec3)
			if err != nil {
				t.Fatal(err)
			}
			boundMdl.Table("uuid_users")
			if err := boundMdl.On(ModelBeforeInsert, func(d map[string]any) bool {
				d["id"] = "uuid-bound-3"
				return true
			}); err != nil {
				t.Fatal(err)
			}
			if err := rec3.Save(); err != nil || rec3.ID != "uuid-bound-3" {
				t.Fatalf("已绑定新记录 Save 应生成并回填 UUID: id=%q err=%v", rec3.ID, err)
			}
		})

		t.Run(dialect.DialectName()+"_setter_generates_key_even_with_omitempty", func(t *testing.T) {
			state := &batch4bSQLState{
				lastInsertID: 1, // 模拟内部 rowid，不得覆盖业务主键
			}
			database := newBatch4bSQLDatabase(t, dialect, state)

			mdl := NewModel(database, "uuid_users")
			if err := mdl.Setter("id", func(v any, _ map[string]any) any {
				if s, ok := v.(string); ok && s == "" {
					return "setter-uuid-001"
				}
				return v
			}); err != nil {
				t.Fatal(err)
			}

			rec := &issue594OmitEmptyStringModel{Name: "Dave"}
			if err := mdl.Create(rec); err != nil {
				t.Fatalf("主键 Setter 生成 UUID 失败: %v", err)
			}
			if rec.ID != "setter-uuid-001" || state.idReads.Load() != 0 {
				t.Fatalf("Setter 主键回填错误或误读 LastInsertId: id=%q reads=%d", rec.ID, state.idReads.Load())
			}
		})

		t.Run(dialect.DialectName()+"_snowflake_int64_not_overwritten_by_last_insert_id", func(t *testing.T) {
			// 模拟 SQLite WITHOUT ROWID 或无 AUTO_INCREMENT 表（LastInsertId 返回 0）
			state := &batch4bSQLState{lastInsertID: 0}
			database := newBatch4bSQLDatabase(t, dialect, state)

			orderMdl := NewModel(database, "orders_norowid")
			if err := orderMdl.On(ModelBeforeInsert, func(d map[string]any) bool {
				d["id"] = int64(9001)
				return true
			}); err != nil {
				t.Fatal(err)
			}

			ord := &issue594SnowflakeOrderModel{Name: "book"}
			if err := orderMdl.Create(ord); err != nil {
				t.Fatalf("雪花主键插入不应报 ErrPartialWrite: %v", err)
			}
			if ord.ID != 9001 || state.idReads.Load() != 0 {
				t.Fatalf("雪花主键被篡改或误读 LastInsertId: id=%d reads=%d", ord.ID, state.idReads.Load())
			}
			queries := state.recordedQueries()
			if len(queries) != 1 || len(queries[0].args) != 2 || queries[0].args[0] != int64(9001) {
				t.Fatalf("INSERT 参数应包含显式雪花主键 9001: %#v", queries)
			}

			// 主键 Setter 生成雪花 ID
			setterMdl := NewModel(database, "orders_norowid")
			if err := setterMdl.Setter("id", func(v any, _ map[string]any) any {
				if id, ok := v.(int64); ok && id == 0 {
					return int64(9002)
				}
				return v
			}); err != nil {
				t.Fatal(err)
			}
			ord2 := &issue594SnowflakeOrderModel{Name: "pen"}
			if err := setterMdl.Create(ord2); err != nil || ord2.ID != 9002 {
				t.Fatalf("主键 Setter 雪花 ID 失败: id=%d err=%v", ord2.ID, err)
			}
		})

		t.Run(dialect.DialectName()+"_still_rejects_unfilled_incompatible_pk_before_sql", func(t *testing.T) {
			state := &batch4bSQLState{lastInsertID: 1}
			database := newBatch4bSQLDatabase(t, dialect, state)

			mdl := NewModel(database, "uuid_users")
			// 钩子未填充主键，仍保持 ""
			if err := mdl.On(ModelBeforeInsert, func(d map[string]any) bool {
				d["name"] = "trimmed"
				return true
			}); err != nil {
				t.Fatal(err)
			}
			rec := &issue594StringUUIDModel{Name: "Alice"}
			err := mdl.Create(rec)
			if !errors.Is(err, ErrInvalidModel) {
				t.Fatalf("未生成业务主键的字符串 ID 在整数驱动上应返回 ErrInvalidModel: %v", err)
			}
			if len(state.recordedQueries()) != 0 {
				t.Fatalf("不兼容零值主键不得发出 SQL: %#v", state.recordedQueries())
			}
		})
	}

	t.Run("backend_without_pk_generation_rejects_zero_pk_unless_hook_fills", func(t *testing.T) {
		conn := &noGenPKConnection{}
		database := NewDB(conn)
		mdl := NewModel(database, "items").PrimaryKey("code")
		item := &struct {
			Code string `thinkgo:"code"`
			Name string `thinkgo:"name"`
		}{Name: "book"}
		if err := mdl.Create(item); !errors.Is(err, ErrInvalidModel) {
			t.Fatalf("后端不支持生成主键时零值应拒绝: %v", err)
		}
		if err := mdl.On(ModelBeforeInsert, func(d map[string]any) bool {
			d["code"] = "BIZ-1"
			return true
		}); err != nil {
			t.Fatal(err)
		}
		if err := mdl.Create(item); err != nil || item.Code != "BIZ-1" {
			t.Fatalf("钩子填充业务主键后应成功: code=%q err=%v", item.Code, err)
		}
	})
}

// ============================================================
// Issue #595 回归测试
// ============================================================

type issue595ValueTimeUser struct {
	*Model
	ID         int64      `thinkgo:"id"`
	Name       string     `thinkgo:"name"`
	CreateTime time.Time  `thinkgo:"create_time"`
	UpdateTime time.Time  `thinkgo:"update_time"`
	DeleteTime *time.Time `thinkgo:"delete_time"`
}

type issue595PtrTimeUser struct {
	*Model
	ID         int64      `thinkgo:"id"`
	Name       string     `thinkgo:"name"`
	CreateTime *time.Time `thinkgo:"create_time"`
	UpdateTime *time.Time `thinkgo:"update_time"`
	DeleteTime *time.Time `thinkgo:"delete_time"`
}

type issue595NullTimeUser struct {
	*Model
	ID         int64        `thinkgo:"id"`
	Name       string       `thinkgo:"name"`
	CreateTime sql.NullTime `thinkgo:"create_time"`
	UpdateTime sql.NullTime `thinkgo:"update_time"`
	DeleteTime sql.NullTime `thinkgo:"delete_time"`
}

type issue595NullableIntUser struct {
	*Model
	ID         int64         `thinkgo:"id"`
	Name       string        `thinkgo:"name"`
	CreateTime *int64        `thinkgo:"create_time"`
	UpdateTime sql.NullInt64 `thinkgo:"update_time"`
	DeleteTime *int64        `thinkgo:"delete_time"`
}

func TestIssue595_AutoTimestampFullLifecycleForTimeTypes(t *testing.T) {
	t0 := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(5 * time.Minute)
	t2 := t0.Add(10 * time.Minute)
	t3 := t0.Add(15 * time.Minute)

	t.Run("value_time_Time_lifecycle", func(t *testing.T) {
		currentNow := t0
		state := &batch4bSQLState{lastInsertID: 101}
		database := newBatch4bSQLDatabase(t, &builder.Sqlite{}, state)
		database.clock = func() time.Time { return currentNow }
		// 保持默认 timestampValueType == "unix"

		u := &issue595ValueTimeUser{Name: "Alice"}
		uMdl, err := NewModelFor(context.Background(), database, u)
		if err != nil {
			t.Fatal(err)
		}
		uMdl.Table("ts_users").AutoTimestamp(true).SoftDelete()

		// 1. Create / Save 插入
		if err := u.Save(); err != nil {
			t.Fatalf("首次插入失败: %v", err)
		}
		if u.ID != 101 || !u.CreateTime.Equal(t0) || !u.UpdateTime.Equal(t0) || u.DeleteTime != nil {
			t.Fatalf("首次插入时间回填错误: %+v", u)
		}

		// 2. 增量 Save 更新（仅修改 Name，未碰 UpdateTime）
		currentNow = t1
		u.Name = "Alice2"
		if err := u.Save(); err != nil {
			t.Fatalf("增量 Save 不应报 ErrPartialWrite: %v", err)
		}
		if !u.CreateTime.Equal(t0) || !u.UpdateTime.Equal(t1) {
			t.Fatalf("增量 Save 后时间不正确: create=%v update=%v", u.CreateTime, u.UpdateTime)
		}
		queries := state.recordedQueries()
		lastUpdate := queries[len(queries)-1]
		for _, arg := range lastUpdate.args {
			if _, isInt64 := arg.(int64); isInt64 && arg == t1.Unix() {
				t.Fatalf("update_time 不应退化为 int64 Unix 秒: args=%#v", lastUpdate.args)
			}
		}

		// 3. Delete 软删除
		currentNow = t2
		if err := u.Delete(); err != nil {
			t.Fatalf("软删除失败: %v", err)
		}
		if u.DeleteTime == nil || !u.DeleteTime.Equal(t2) || !u.UpdateTime.Equal(t2) {
			t.Fatalf("软删除时间回填错误: delete=%v update=%v", u.DeleteTime, u.UpdateTime)
		}

		// 4. Restore 恢复软删除
		currentNow = t3
		if err := u.Restore(); err != nil {
			t.Fatalf("恢复软删除失败: %v", err)
		}
		if u.DeleteTime != nil || !u.UpdateTime.Equal(t3) {
			t.Fatalf("恢复软删除后状态错误: delete=%v update=%v", u.DeleteTime, u.UpdateTime)
		}

		// 恢复后实例未锁死，仍可继续增量更新
		u.Name = "Alice3"
		if err := u.Save(); err != nil {
			t.Fatalf("恢复后再次增量 Save 失败: %v", err)
		}
	})

	t.Run("pointer_time_Time_lifecycle", func(t *testing.T) {
		currentNow := t0
		state := &batch4bSQLState{lastInsertID: 102}
		database := newBatch4bSQLDatabase(t, &builder.Sqlite{}, state)
		database.clock = func() time.Time { return currentNow }

		u := &issue595PtrTimeUser{Name: "Bob"}
		uMdl, err := NewModelFor(context.Background(), database, u)
		if err != nil {
			t.Fatal(err)
		}
		uMdl.Table("ts_users").AutoTimestamp(true).SoftDelete()

		if err := u.Save(); err != nil {
			t.Fatalf("*time.Time 零值首次插入失败: %v", err)
		}
		if u.CreateTime == nil || !u.CreateTime.Equal(t0) || u.UpdateTime == nil || !u.UpdateTime.Equal(t0) || u.DeleteTime != nil {
			t.Fatalf("*time.Time 插入回填错误: %+v", u)
		}

		currentNow = t1
		u.Name = "Bob2"
		if err := u.Save(); err != nil {
			t.Fatalf("*time.Time 增量更新失败: %v", err)
		}
		if !u.UpdateTime.Equal(t1) {
			t.Fatalf("*time.Time 更新时间错误: %v", u.UpdateTime)
		}

		currentNow = t2
		if err := u.Delete(); err != nil {
			t.Fatalf("*time.Time 软删除失败: %v", err)
		}
		if u.DeleteTime == nil || !u.DeleteTime.Equal(t2) || !u.UpdateTime.Equal(t2) {
			t.Fatalf("*time.Time 软删除回填错误: delete=%v update=%v", u.DeleteTime, u.UpdateTime)
		}

		currentNow = t3
		if err := u.Restore(); err != nil {
			t.Fatalf("*time.Time 恢复失败: %v", err)
		}
		if u.DeleteTime != nil || !u.UpdateTime.Equal(t3) {
			t.Fatalf("*time.Time 恢复回填错误: delete=%v update=%v", u.DeleteTime, u.UpdateTime)
		}
	})

	t.Run("sql_NullTime_lifecycle_and_scanned_record", func(t *testing.T) {
		currentNow := t0
		state := &batch4bSQLState{
			lastInsertID: 103,
			queryHandler: func(_ string, _ []driver.Value) ([]string, [][]driver.Value, error) {
				return []string{"id", "name", "create_time", "update_time", "delete_time"},
					[][]driver.Value{{int64(103), "Carol", t0, t0, nil}}, nil
			},
		}
		database := newBatch4bSQLDatabase(t, &builder.Sqlite{}, state)
		database.clock = func() time.Time { return currentNow }

		tableMdl := NewModel(database, "ts_users").AutoTimestamp(true).SoftDelete()

		created := &issue595NullTimeUser{Name: "Carol"}
		if err := tableMdl.Create(created); err != nil {
			t.Fatalf("sql.NullTime 首次创建失败: %v", err)
		}

		// 通过通用 NewModel 查询绑定记录（测试 bindScannedRecord 记录结构体类型）
		var loaded issue595NullTimeUser
		found, err := tableMdl.Where("id", 103).Find(&loaded)
		if err != nil || !found {
			t.Fatalf("查询 sql.NullTime 记录失败: found=%v err=%v", found, err)
		}

		currentNow = t1
		loaded.Name = "Carol2"
		if err := loaded.Save(); err != nil {
			t.Fatalf("sql.NullTime 增量 Save 失败: %v", err)
		}
		if !loaded.UpdateTime.Valid || !loaded.UpdateTime.Time.Equal(t1) {
			t.Fatalf("sql.NullTime 更新时间错误: %+v", loaded.UpdateTime)
		}

		currentNow = t2
		if err := loaded.Delete(); err != nil {
			t.Fatalf("sql.NullTime 软删除失败: %v", err)
		}
		if !loaded.DeleteTime.Valid || !loaded.DeleteTime.Time.Equal(t2) {
			t.Fatalf("sql.NullTime 软删除时间错误: %+v", loaded.DeleteTime)
		}

		currentNow = t3
		if err := loaded.Restore(); err != nil {
			t.Fatalf("sql.NullTime 恢复失败: %v", err)
		}
		if loaded.DeleteTime.Valid || !loaded.UpdateTime.Valid || !loaded.UpdateTime.Time.Equal(t3) {
			t.Fatalf("sql.NullTime 恢复后状态错误: delete=%+v update=%+v", loaded.DeleteTime, loaded.UpdateTime)
		}
	})

	t.Run("nullable_int64_and_sql_null_variants_in_setAutoTimestamp", func(t *testing.T) {
		currentNow := t0
		state := &batch4bSQLState{lastInsertID: 104}
		database := newBatch4bSQLDatabase(t, &builder.Sqlite{}, state)
		database.clock = func() time.Time { return currentNow }

		u := &issue595NullableIntUser{Name: "Dave"}
		uMdl, err := NewModelFor(context.Background(), database, u)
		if err != nil {
			t.Fatal(err)
		}
		uMdl.Table("ts_int_users").AutoTimestamp(true).SoftDelete()
		if err := u.Save(); err != nil {
			t.Fatalf("*int64/sql.NullInt64 首次保存失败: %v", err)
		}
		if u.CreateTime == nil || *u.CreateTime != t0.Unix() || !u.UpdateTime.Valid || u.UpdateTime.Int64 != t0.Unix() {
			t.Fatalf("可空整型时间戳回填错误: %+v", u)
		}

		// 直接覆盖 setAutoTimestamp 各指针与 sql.Null* 分支及溢出边界
		nowSmall := time.Unix(100, 0).UTC()
		explicitTime := time.Unix(50, 0).UTC()
		explicitInt := int64(55)
		zeroInt := int64(0)
		cases := []struct {
			name string
			in   any
			want any
		}{
			{"nil *time.Time", (*time.Time)(nil), nowSmall},
			{"explicit *time.Time", &explicitTime, &explicitTime},
			{"nil *int64", (*int64)(nil), int64(100)},
			{"zero *int64", &zeroInt, int64(100)},
			{"explicit *int64", &explicitInt, &explicitInt},
			{"nil *int32", (*int32)(nil), int32(100)},
			{"nil *uint32", (*uint32)(nil), uint32(100)},
			{"nil *string", (*string)(nil), "100"},
			{"NullTime invalid", sql.NullTime{}, nowSmall},
			{"NullTime valid", sql.NullTime{Time: explicitTime, Valid: true}, sql.NullTime{Time: explicitTime, Valid: true}},
			{"*NullTime nil", (*sql.NullTime)(nil), nowSmall},
			{"NullInt64 invalid", sql.NullInt64{}, int64(100)},
			{"NullInt32 invalid", sql.NullInt32{}, int32(100)},
			{"NullInt16 invalid", sql.NullInt16{}, int16(100)},
			{"NullByte invalid", sql.NullByte{}, uint8(100)},
			{"NullString invalid", sql.NullString{}, "100"},
		}
		for _, tc := range cases {
			data := map[string]any{"ts": tc.in}
			if err := setAutoTimestamp(data, "ts", nowSmall, TimestampValueTypeUnix); err != nil {
				t.Fatalf("%s 失败: %v", tc.name, err)
			}
			if !reflect.DeepEqual(data["ts"], tc.want) {
				t.Fatalf("%s 结果不符: got=%#v want=%#v", tc.name, data["ts"], tc.want)
			}
		}

		// 窄 Null* 溢出检查与不支持的指针零值类型检查
		hugeTime := time.Unix(1<<40, 0)
		for _, val := range []any{sql.NullInt32{}, sql.NullInt16{}, sql.NullByte{}} {
			if err := setAutoTimestamp(map[string]any{"ts": val}, "ts", hugeTime, TimestampValueTypeUnix); !errors.Is(err, ErrTimestampOverflow) {
				t.Fatalf("窄可空整数 %T 溢出应返回 ErrTimestampOverflow: %v", val, err)
			}
		}
		type unsupportedTS struct{ X int }
		if err := setAutoTimestamp(map[string]any{"ts": (*unsupportedTS)(nil)}, "ts", nowSmall, TimestampValueTypeUnix); !errors.Is(err, ErrInvalidDatabaseConfig) {
			t.Fatalf("不支持的类型化空指针应返回 ErrInvalidDatabaseConfig: %v", err)
		}
	})
}

// ============================================================
// Issue #596 回归测试
// ============================================================

type failingValuerKey struct{}

func (failingValuerKey) Value() (driver.Value, error) {
	return nil, errors.New("broken key valuer")
}

func TestIssue596_ImmediateRelationsShortCircuitOnNilKeys(t *testing.T) {
	state := &batch4bSQLState{
		queryHandler: func(query string, _ []driver.Value) ([]string, [][]driver.Value, error) {
			if strings.Contains(query, "IS NULL") {
				return []string{"id", "user_id", "title"},
					[][]driver.Value{{int64(10), nil, "orphan_secret"}}, nil
			}
			return []string{"id", "user_id", "title"},
				[][]driver.Value{{int64(20), int64(7), "owned_post"}}, nil
		},
	}
	database := newBatch4bSQLDatabase(t, &builder.Sqlite{}, state)
	users := NewModel(database, "users")
	profiles := NewModel(database, "profiles")
	posts := NewModel(database, "posts")
	orgs := NewModel(database, "orgs")
	roles := NewModel(database, "roles")

	var typedNilInt *int64
	var typedNilStr *string
	var doubleNilInt **int64
	nullKeys := []struct {
		name string
		key  any
	}{
		{"untyped_nil", nil},
		{"typed_nil_int64", typedNilInt},
		{"typed_nil_string", typedNilStr},
		{"double_nil_int64", doubleNilInt},
		{"sql_NullInt64_invalid", sql.NullInt64{Valid: false}},
		{"sql_NullString_invalid", sql.NullString{Valid: false}},
		{"sql_NullTime_invalid", sql.NullTime{Valid: false}},
	}

	for _, nk := range nullKeys {
		t.Run(nk.name, func(t *testing.T) {
			before := len(state.recordedQueries())

			one, err := users.HasOne(profiles, "user_id", nk.key)
			if err != nil || one != nil {
				t.Fatalf("HasOne(%s) 应返回 nil, nil: got=%#v err=%v", nk.name, one, err)
			}

			many, err := users.HasMany(posts, "user_id", nk.key)
			if err != nil || many == nil || len(many) != 0 {
				t.Fatalf("HasMany(%s) 应返回非 nil 空切片: got=%#v err=%v", nk.name, many, err)
			}

			owner, err := users.BelongsTo(orgs, nk.key, "id")
			if err != nil || owner != nil {
				t.Fatalf("BelongsTo(%s) 应返回 nil, nil: got=%#v err=%v", nk.name, owner, err)
			}

			manyRoles, err := users.BelongsToMany(roles, "user_role", "user_id", "role_id", nk.key)
			if err != nil || manyRoles == nil || len(manyRoles) != 0 {
				t.Fatalf("BelongsToMany(%s) 应返回非 nil 空切片: got=%#v err=%v", nk.name, manyRoles, err)
			}

			if executed := len(state.recordedQueries()) - before; executed != 0 {
				t.Fatalf("空关联键 %s 不得发出任何 SQL 查询，实际执行了 %d 次: %#v", nk.name, executed, state.recordedQueries()[before:])
			}
		})
	}

	// 非空指针与有效 sql.NullInt64（含指针）仍可正常查询关联数据
	validID := int64(7)
	validNullInt := sql.NullInt64{Int64: 7, Valid: true}
	if rows, err := users.HasMany(posts, "user_id", &validID); err != nil || len(rows) != 1 || rows[0]["title"] != "owned_post" {
		t.Fatalf("非空指针关联键查询失败: rows=%#v err=%v", rows, err)
	}
	if row, err := users.HasOne(posts, "user_id", &validNullInt); err != nil || row["title"] != "owned_post" {
		t.Fatalf("有效 *sql.NullInt64 关联键查询失败: row=%#v err=%v", row, err)
	}

	// 非法标识符或错误 Valuer 即使关联键为空/异常也必须明确报错
	if _, err := users.HasMany(posts, "bad key", nil); !errors.Is(err, ErrInvalidRelation) {
		t.Fatalf("非法外键标识符应返回 ErrInvalidRelation: %v", err)
	}
	if _, err := users.BelongsToMany(roles, "bad table", "user_id", "role_id", nil); !errors.Is(err, ErrInvalidRelation) {
		t.Fatalf("非法中间表标识符应返回 ErrInvalidRelation: %v", err)
	}
	if _, err := users.HasOne(posts, "user_id", failingValuerKey{}); !errors.Is(err, ErrInvalidRelation) {
		t.Fatalf("报错的 Valuer 关联键应返回 ErrInvalidRelation: %v", err)
	}
	failingPtr := &failingValuerKey{}
	if _, err := users.BelongsTo(orgs, failingPtr, "id"); !errors.Is(err, ErrInvalidRelation) {
		t.Fatalf("报错的指针 Valuer 关联键应返回 ErrInvalidRelation: %v", err)
	}
}

// ============================================================
// Issue #597 回归测试
// ============================================================

type issue597PartialAccount struct {
	*Model
	ID      int64  `thinkgo:"id"`
	Name    string `thinkgo:"name"`
	Balance int64  `thinkgo:"balance"`
}

func TestIssue597_TableModelSaveAndUpdateRespectBoundRecordGuards(t *testing.T) {
	c := &recordLifecycleConnection{
		rows:  []map[string]any{{"id": int64(1), "name": "Ada"}}, // 部分列投影，未加载 balance=5000
		count: 1,
	}
	database := NewDB(c)
	accMdl := NewModel(database, "accounts")

	var acc issue597PartialAccount
	found, err := accMdl.Field("id, name").Where("id", 1).Find(&acc)
	if err != nil || !found {
		t.Fatalf("部分列查询失败: found=%v err=%v", found, err)
	}
	if acc.Balance != 0 {
		t.Fatalf("未投影字段初始应为零值: %d", acc.Balance)
	}

	// 1. tableModel.Save(&acc) 仅增量更新已修改的 name，绝不清零 balance
	acc.Name = "Ada-Updated"
	if err := accMdl.Save(&acc); err != nil {
		t.Fatalf("accMdl.Save(&acc) 失败: %v", err)
	}
	if _, leaked := c.lastData["balance"]; leaked {
		t.Fatalf("accMdl.Save(&acc) 静默写入了未加载列 balance: %#v", c.lastData)
	}
	if c.lastData["name"] != "Ada-Updated" || len(c.lastData) != 1 {
		t.Fatalf("增量更新载荷错误: %#v", c.lastData)
	}

	// 快照已同步，再次调用 accMdl.Save(&acc) 不再发出写操作
	c.lastData = nil
	if err := accMdl.Save(&acc); err != nil || c.lastData != nil {
		t.Fatalf("快照未同步导致重复写入: data=%#v err=%v", c.lastData, err)
	}

	// 2. tableModel.Update(&acc) 同样走受保护增量更新
	acc.Name = "Ada-Updated-2"
	if err := accMdl.Update(&acc); err != nil {
		t.Fatalf("accMdl.Update(&acc) 失败: %v", err)
	}
	if _, leaked := c.lastData["balance"]; leaked {
		t.Fatalf("accMdl.Update(&acc) 静默写入了未加载列 balance: %#v", c.lastData)
	}
	if c.lastData["name"] != "Ada-Updated-2" || len(c.lastData) != 1 {
		t.Fatalf("Update 增量更新载荷错误: %#v", c.lastData)
	}

	// 3. 修改未加载列 balance 必须触发 ErrModelFieldNotLoaded
	acc.Balance = 100
	if err := accMdl.Save(&acc); !errors.Is(err, ErrModelFieldNotLoaded) {
		t.Fatalf("修改未加载列调用 Save 应报 ErrModelFieldNotLoaded: %v", err)
	}
	if err := accMdl.Update(&acc); !errors.Is(err, ErrModelFieldNotLoaded) {
		t.Fatalf("修改未加载列调用 Update 应报 ErrModelFieldNotLoaded: %v", err)
	}
	acc.Balance = 0 // 恢复未加载列原零值

	// 4. 篡改主键 ID 必须触发 ErrModelIdentityChanged，禁止覆写其他行
	acc.ID = 999
	if err := accMdl.Save(&acc); !errors.Is(err, ErrModelIdentityChanged) {
		t.Fatalf("篡改主键后 accMdl.Save(&acc) 应报 ErrModelIdentityChanged: %v", err)
	}
	if err := accMdl.Update(&acc); !errors.Is(err, ErrModelIdentityChanged) {
		t.Fatalf("篡改主键后 accMdl.Update(&acc) 应报 ErrModelIdentityChanged: %v", err)
	}
	if err := accMdl.Delete(&acc); !errors.Is(err, ErrModelIdentityChanged) {
		t.Fatalf("篡改主键后 accMdl.Delete(&acc) 应报 ErrModelIdentityChanged: %v", err)
	}
	acc.ID = 1

	// 5. 跨表、跨库或对已持久化记录重复 Create 必须拦截
	if err := NewModel(database, "other_accounts").Save(&acc); !errors.Is(err, ErrModelIdentityChanged) {
		t.Fatalf("跨表 Save 已绑定记录应报 ErrModelIdentityChanged: %v", err)
	}
	if err := NewModel(NewDB(&recordLifecycleConnection{}), "accounts").Save(&acc); !errors.Is(err, ErrModelIdentityChanged) {
		t.Fatalf("跨库 Save 已绑定记录应报 ErrModelIdentityChanged: %v", err)
	}
	if err := accMdl.Create(&acc); !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("已持久化记录调用 accMdl.Create(&acc) 应报 ErrInvalidModel: %v", err)
	}

	// 6. 删除后状态守卫：accMdl.Delete(&acc) 后禁止继续 Save / Update
	if err := accMdl.Delete(&acc); err != nil {
		t.Fatalf("accMdl.Delete(&acc) 失败: %v", err)
	}
	acc.Name = "Ghost"
	if err := accMdl.Save(&acc); !errors.Is(err, ErrModelRecordMissing) {
		t.Fatalf("已删除记录调用 accMdl.Save(&acc) 应报 ErrModelRecordMissing: %v", err)
	}
	if err := accMdl.Update(&acc); !errors.Is(err, ErrModelRecordMissing) {
		t.Fatalf("已删除记录调用 accMdl.Update(&acc) 应报 ErrModelRecordMissing: %v", err)
	}
}

func TestIssue597_TransactionIsolationAndUnpersistedGuards(t *testing.T) {
	state := &batch4bSQLState{
		lastInsertID: 88,
		queryHandler: func(_ string, _ []driver.Value) ([]string, [][]driver.Value, error) {
			return []string{"id", "name"}, [][]driver.Value{{int64(1), "Ada"}}, nil
		},
	}
	database := newBatch4bSQLDatabase(t, &builder.Sqlite{}, state)
	accMdl := NewModel(database, "accounts")

	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()

	var acc issue597PartialAccount
	found, err := accMdl.WithTx(tx).Field("id, name").Where("id", 1).Find(&acc)
	if err != nil || !found {
		t.Fatalf("事务内查询失败: found=%v err=%v", found, err)
	}

	acc.Name = "Tx-Updated"
	// 未提交事务外通过表模型保存必须报 ErrModelRecordTransaction
	if err := accMdl.Save(&acc); !errors.Is(err, ErrModelRecordTransaction) {
		t.Fatalf("未提交事务记录在事务外调用 accMdl.Save(&acc) 应报 ErrModelRecordTransaction: %v", err)
	}
	// 显式绑定同一事务的 accMdl.WithTx(tx).Save(&acc) 允许保存
	if err := accMdl.WithTx(tx).Save(&acc); err != nil {
		t.Fatalf("同一事务内 accMdl.WithTx(tx).Save(&acc) 应成功: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// 提交后非事务 accMdl.Save(&acc) 可正常保存
	acc.Name = "After-Commit"
	if err := accMdl.Save(&acc); err != nil {
		t.Fatalf("事务提交后 accMdl.Save(&acc) 应成功: %v", err)
	}

	// 新绑定未持久化记录通过 Update 或错表 Create 的守卫
	fresh := &issue597PartialAccount{Name: "Fresh"}
	freshMdl, err := NewModelFor(context.Background(), database, fresh)
	if err != nil {
		t.Fatal(err)
	}
	freshMdl.Table("accounts")
	if err := accMdl.Update(fresh); !errors.Is(err, ErrModelIdentityMissing) {
		t.Fatalf("未持久化记录调用 Update 应报 ErrModelIdentityMissing: %v", err)
	}
	if err := NewModel(database, "wrong_table").Create(fresh); !errors.Is(err, ErrModelIdentityChanged) {
		t.Fatalf("未持久化记录跨表 Create 应报 ErrModelIdentityChanged: %v", err)
	}
	if err := fresh.Save(&acc); !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("已绑定其他记录的模型句柄调用 Save(other) 应报 ErrInvalidModel: %v", err)
	}

	// 绑定实例上的 SoftDelete/AutoTimestamp 配置在通过裸 DAO 表模型 Save/Delete 时仍应继承生效
	tsRec := &issue595ValueTimeUser{Name: "Inherited"}
	tsBound, err := NewModelFor(context.Background(), database, tsRec)
	if err != nil {
		t.Fatal(err)
	}
	tsBound.Table("accounts").AutoTimestamp(true).SoftDelete()
	bareDAO := NewModel(database, "accounts")
	if err := bareDAO.Save(tsRec); err != nil || tsRec.CreateTime.IsZero() {
		t.Fatalf("裸 DAO Save 新绑定记录应继承 AutoTimestamp: rec=%+v err=%v", tsRec, err)
	}
	if err := bareDAO.Delete(tsRec); err != nil || tsRec.DeleteTime == nil {
		t.Fatalf("裸 DAO Delete 软删除绑定记录应继承 SoftDelete: rec=%+v err=%v", tsRec, err)
	}

	// 无绑定普通结构体的 Delete 边界校验
	plain := &structUpdateUser{ID: 7, Name: "plain"}
	if err := accMdl.Delete(plain); err != nil {
		t.Fatalf("按主键删除普通结构体应成功: %v", err)
	}
	if err := accMdl.Delete(&structUpdateUser{Name: "zero"}); !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("零值主键删除普通结构体应返回 ErrInvalidModel: %v", err)
	}
	if err := accMdl.Delete(plain, plain); !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("多参数 Delete 应返回 ErrInvalidModel: %v", err)
	}
}

// ============================================================
// Issue #598 回归测试
// ============================================================

type issue598OrgCode string
type issue598ActiveFlag bool

type issue598DeptDTO struct {
	ID   int64  `thinkgo:"id"`
	Code string `thinkgo:"code"`
	Name string `thinkgo:"name"`
}

type issue598EmpRow struct {
	*Model
	ID       int64            `thinkgo:"id"`
	DeptCode issue598OrgCode  `thinkgo:"dept_code"`
	Dept     *issue598DeptDTO `thinkgo:"dept,readonly"`
}

type issue598SecretText struct {
	raw []byte
}

func (s *issue598SecretText) Value() (driver.Value, error) {
	if s == nil {
		return nil, nil
	}
	return "enc:" + string(s.raw), nil
}

func (s *issue598SecretText) Scan(src any) error {
	var text string
	switch v := src.(type) {
	case string:
		text = v
	case []byte:
		text = string(v)
	default:
		return fmt.Errorf("unsupported scan source %T", src)
	}
	s.raw = []byte(strings.TrimPrefix(text, "enc:"))
	return nil
}

type issue598ExportedPtrValuer struct {
	Label string
}

func (v *issue598ExportedPtrValuer) Value() (driver.Value, error) {
	if v == nil {
		return nil, nil
	}
	return "label:" + v.Label, nil
}

func (v *issue598ExportedPtrValuer) Scan(src any) error {
	if s, ok := src.(string); ok {
		v.Label = strings.TrimPrefix(s, "label:")
		return nil
	}
	return fmt.Errorf("unexpected %T", src)
}

type issue598SecretItem struct {
	*Model
	ID     int64                     `thinkgo:"id"`
	Secret issue598SecretText        `thinkgo:"secret"`
	Tag    issue598ExportedPtrValuer `thinkgo:"tag"`
}

type issue598BrokenOpaqueValuer struct {
	raw []byte
	bad bool
}

func (b *issue598BrokenOpaqueValuer) Value() (driver.Value, error) {
	if b.bad {
		return struct{}{}, nil
	}
	return nil, errors.New("valuer boom")
}

func TestIssue598_NamedStringBoolRelationKeysAndClonedRelationMaps(t *testing.T) {
	connection := &modelBusinessConnection{
		tableRows: map[string][]map[string]any{
			"emps": {
				{"id": int64(1), "dept_code": issue598OrgCode("ENG"), "active_flag": issue598ActiveFlag(true)},
				{"id": int64(2), "dept_code": issue598OrgCode("ENG"), "active_flag": issue598ActiveFlag(true)},
			},
			"depts": {
				{"id": int64(10), "code": "ENG", "name": "Engineering", "badge": []byte("eng")},
			},
			"flags": {
				{"flag": true, "label": "enabled"},
			},
			"emp_roles": {
				{"emp_code": issue598OrgCode("ENG"), "role_code": issue598OrgCode("R_ADMIN")},
			},
			"roles": {
				{"code": "R_ADMIN", "title": "Administrator"},
			},
		},
	}
	database := NewDB(connection)
	empMdl := NewModel(database, "emps")
	deptMdl := NewModel(database, "depts").PrimaryKey("code")
	flagMdl := NewModel(database, "flags").PrimaryKey("flag")
	roleMdl := NewModel(database, "roles").PrimaryKey("code")

	if err := empMdl.DefineBelongsTo("dept", deptMdl, "dept_code", "code"); err != nil {
		t.Fatal(err)
	}
	if err := empMdl.DefineBelongsTo("flag_info", flagMdl, "active_flag", "flag"); err != nil {
		t.Fatal(err)
	}
	if err := empMdl.DefineHasOne("primary_dept", deptMdl, "code", "dept_code"); err != nil {
		t.Fatal(err)
	}
	if err := empMdl.DefineHasMany("dept_list", deptMdl, "code", "dept_code"); err != nil {
		t.Fatal(err)
	}
	if err := empMdl.DefineBelongsToMany("roles", roleMdl, "emp_roles", "emp_code", "role_code", "dept_code"); err != nil {
		t.Fatal(err)
	}

	// 1. 结构体 Find 预加载具名 string 关联键
	var emp issue598EmpRow
	found, err := empMdl.With("dept").Where("id", 1).Find(&emp)
	if err != nil || !found || emp.Dept == nil || emp.Dept.Name != "Engineering" {
		t.Fatalf("具名 string 外键预加载失败: found=%v dept=%+v err=%v", found, emp.Dept, err)
	}

	// 2. SelectMaps 验证具名 string/bool 预加载成功且多父行关联 map 完全隔离
	rows, err := empMdl.With("dept", "flag_info", "primary_dept", "dept_list", "roles").SelectMaps()
	if err != nil || len(rows) != 2 {
		t.Fatalf("SelectMaps 预加载失败: rows=%#v err=%v", rows, err)
	}
	dept0 := rows[0]["dept"].(map[string]any)
	dept1 := rows[1]["dept"].(map[string]any)
	if dept0["name"] != "Engineering" || dept1["name"] != "Engineering" {
		t.Fatalf("部门预加载值错误: %#v / %#v", dept0, dept1)
	}
	if rows[0]["flag_info"].(map[string]any)["label"] != "enabled" {
		t.Fatalf("具名 bool 关联预加载失败: %#v", rows[0]["flag_info"])
	}

	// 修改第一行的关联 map，不得污染第二行
	dept0["masked"] = true
	dept0["badge"].([]byte)[0] = 'X'
	if _, polluted := dept1["masked"]; polluted || string(dept1["badge"].([]byte)) != "eng" {
		t.Fatalf("BelongsTo 跨父行共享了可变关联 map: row1=%#v", dept1)
	}

	roles0 := rows[0]["roles"].([]map[string]any)
	roles1 := rows[1]["roles"].([]map[string]any)
	if len(roles0) != 1 || len(roles1) != 1 || roles0[0]["title"] != "Administrator" {
		t.Fatalf("BelongsToMany 具名关联键加载失败: %#v / %#v", roles0, roles1)
	}
	roles0[0]["masked"] = true
	if _, polluted := roles1[0]["masked"]; polluted {
		t.Fatalf("BelongsToMany 跨父行共享了可变关联 map: %#v", roles1[0])
	}

	hasOne0 := rows[0]["primary_dept"].(map[string]any)
	hasOne1 := rows[1]["primary_dept"].(map[string]any)
	hasOne0["mutated"] = 1
	if _, polluted := hasOne1["mutated"]; polluted {
		t.Fatalf("HasOne 跨父行共享了可变关联 map: %#v", hasOne1)
	}

	hasMany0 := rows[0]["dept_list"].([]map[string]any)
	hasMany1 := rows[1]["dept_list"].([]map[string]any)
	hasMany0[0]["mutated"] = 1
	if _, polluted := hasMany1[0]["mutated"]; polluted {
		t.Fatalf("HasMany 跨父行共享了可变关联 map: %#v", hasMany1)
	}
}

func TestIssue598_PointerReceiverValuerValueFieldWriteAndScan(t *testing.T) {
	var storedSecret, storedTag any
	state := &batch4bSQLState{
		lastInsertID: 55,
		execHandler: func(query string, args []driver.Value) (int64, error) {
			if strings.HasPrefix(query, "INSERT") {
				// 列按字母序: secret, tag
				storedSecret, storedTag = args[0], args[1]
			} else if strings.HasPrefix(query, "UPDATE") {
				storedSecret = args[0]
			}
			return 1, nil
		},
		queryHandler: func(_ string, _ []driver.Value) ([]string, [][]driver.Value, error) {
			return []string{"id", "secret", "tag"},
				[][]driver.Value{{int64(55), storedSecret, storedTag}}, nil
		},
	}
	database := newBatch4bSQLDatabase(t, &builder.Sqlite{}, state)
	secMdl := NewModel(database, "secrets")

	rawBuf := []byte("top-secret")
	item := &issue598SecretItem{
		Secret: issue598SecretText{raw: rawBuf},
		Tag:    issue598ExportedPtrValuer{Label: "v1"},
	}
	if err := secMdl.Create(item); err != nil {
		t.Fatalf("指针接收者 driver.Valuer 值字段 Create 失败: %v", err)
	}
	rawBuf[0] = 'X' // 修改外部输入切片不得影响已落库值
	if item.ID != 55 || storedSecret != "enc:top-secret" || storedTag != "label:v1" {
		t.Fatalf("指针接收者 Valuer 序列化结果错误: id=%d secret=%#v tag=%#v", item.ID, storedSecret, storedTag)
	}

	// 查询绑定并增量 Save 修改值字段
	var loaded issue598SecretItem
	found, err := secMdl.Where("id", 55).Find(&loaded)
	if err != nil || !found || string(loaded.Secret.raw) != "top-secret" || loaded.Tag.Label != "v1" {
		t.Fatalf("扫描回填指针接收者 Scanner 失败: found=%v loaded=%+v err=%v", found, loaded, err)
	}
	loaded.Secret = issue598SecretText{raw: []byte("rotated-secret")}
	if err := loaded.Save(); err != nil {
		t.Fatalf("已绑定记录增量 Save 指针接收者 Valuer 字段失败: %v", err)
	}
	if storedSecret != "enc:rotated-secret" {
		t.Fatalf("增量更新后 Valuer 未生效: %#v", storedSecret)
	}

	// 非绑定结构体 Update 同样生效
	if err := secMdl.Update(&issue598SecretItem{
		ID:     55,
		Secret: issue598SecretText{raw: []byte("direct-update")},
		Tag:    issue598ExportedPtrValuer{Label: "v2"},
	}); err != nil {
		t.Fatalf("非绑定 Update 指针接收者 Valuer 失败: %v", err)
	}

	// 错误或返回非法类型的私有状态 Valuer 必须安全报错
	if _, err := secMdl.Insert(map[string]any{"secret": issue598BrokenOpaqueValuer{raw: []byte("x")}}); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("Valuer 返回错误时应包装为 ErrInvalidQuery: %v", err)
	}
	if _, err := secMdl.Insert(map[string]any{"secret": issue598BrokenOpaqueValuer{raw: []byte("x"), bad: true}}); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("Valuer 返回非法 driver.Value 时应返回 ErrInvalidQuery: %v", err)
	}
}

func TestIssue598_SoftDeleteQualifiesMainTableOnJoin(t *testing.T) {
	state := &batch4bSQLState{
		queryHandler: func(query string, _ []driver.Value) ([]string, [][]driver.Value, error) {
			// 模拟关联表也包含 delete_time 列：若 SQL 出现裸 "delete_time" IS NULL 则模拟 SQLite ambiguous column name 报错
			if strings.Contains(query, "JOIN") && !strings.Contains(query, `"tg_emps"."delete_time"`) && !strings.Contains(query, `"emps"."delete_time"`) {
				return nil, nil, errors.New("ambiguous column name: delete_time")
			}
			return []string{"id", "name", "delete_time"},
				[][]driver.Value{{int64(1), "Alice", nil}}, nil
		},
	}
	database := newBatch4bSQLDatabase(t, &builder.Sqlite{}, state)
	database.prefix = "tg_"

	empMdl := NewModel(database, "emps").SoftDelete()

	// 1. 默认软删除 + Join 自动限定主表名（含前缀）
	qActive := empMdl.Join("depts", "tg_emps.dept_id = tg_depts.id").PrepareQuery()
	sqlActive, _, err := qActive.BuildSelectSQL()
	if err != nil {
		t.Fatalf("构建软删除 Join SQL 失败: %v", err)
	}
	if !strings.Contains(sqlActive, `"tg_emps"."delete_time" IS NULL`) {
		t.Fatalf("Join 下软删除条件未限定主表名: %s", sqlActive)
	}

	// 2. OnlyTrashed + LeftJoin / RightJoin 自动限定主表名
	qTrashed := empMdl.OnlyTrashed().LeftJoin("depts", "tg_emps.dept_id = tg_depts.id").PrepareQuery()
	sqlTrashed, _, err := qTrashed.BuildSelectSQL()
	if err != nil {
		t.Fatalf("构建 OnlyTrashed Join SQL 失败: %v", err)
	}
	if !strings.Contains(sqlTrashed, `"tg_emps"."delete_time" IS NOT NULL`) {
		t.Fatalf("Join 下 OnlyTrashed 条件未限定主表名: %s", sqlTrashed)
	}
	if sqlLeft, _, err := empMdl.LeftJoin("depts", "tg_emps.dept_id = tg_depts.id").PrepareQuery().BuildSelectSQL(); err != nil || !strings.Contains(sqlLeft, `"tg_emps"."delete_time" IS NULL`) {
		t.Fatalf("Model.LeftJoin 软删除限定错误: sql=%s err=%v", sqlLeft, err)
	}
	if sqlRight, _, err := empMdl.RightJoin("depts", "tg_emps.dept_id = tg_depts.id").PrepareQuery().BuildSelectSQL(); err != nil || !strings.Contains(sqlRight, `"tg_emps"."delete_time" IS NULL`) {
		t.Fatalf("Model.RightJoin 软删除限定错误: sql=%s err=%v", sqlRight, err)
	}

	// 3. WithTrashed 不注入 delete_time 条件
	qWithTrashed := empMdl.WithTrashed().Join("depts", "tg_emps.dept_id = tg_depts.id").PrepareQuery()
	sqlWithTrashed, _, err := qWithTrashed.BuildSelectSQL()
	if err != nil || strings.Contains(sqlWithTrashed, "delete_time") {
		t.Fatalf("WithTrashed 不应注入 delete_time 条件: sql=%s err=%v", sqlWithTrashed, err)
	}

	// 4. 实际执行 Join 查询与 Chunk 遍历（验证默认主键排序同样限定主表名）
	rows, err := empMdl.Join("depts", "tg_emps.dept_id = tg_depts.id").SelectMaps()
	if err != nil || len(rows) != 1 {
		t.Fatalf("软删除 Join 实际查询失败: rows=%#v err=%v", rows, err)
	}

	type empJoinDTO struct {
		ID   int64  `thinkgo:"id"`
		Name string `thinkgo:"name"`
	}
	var chunked []empJoinDTO
	if err := Chunk[empJoinDTO](empMdl.Join("depts", "tg_emps.dept_id = tg_depts.id"), 10, func(batch []empJoinDTO) (bool, error) {
		chunked = append(chunked, batch...)
		return false, nil
	}); err != nil || len(chunked) != 1 {
		t.Fatalf("软删除 Join Chunk 查询失败: chunked=%#v err=%v", chunked, err)
	}
}

func TestIssueBatch4b_RealSQLiteEndToEnd(t *testing.T) {
	database, raw := newModelScanSQLite(t)
	if _, err := raw.Exec(`
		CREATE TABLE orders_norowid (id INTEGER PRIMARY KEY, name TEXT NOT NULL) WITHOUT ROWID;
		CREATE TABLE uuid_items (id TEXT PRIMARY KEY, name TEXT NOT NULL);
		CREATE TABLE ts_users (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, create_time DATETIME NOT NULL, update_time DATETIME NOT NULL, delete_time DATETIME NULL);
		CREATE TABLE posts (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NULL, title TEXT NOT NULL);
		INSERT INTO posts(id, user_id, title) VALUES (10, NULL, 'orphan_secret');
		CREATE TABLE accounts (id INTEGER PRIMARY KEY, name TEXT NOT NULL, balance INTEGER NOT NULL);
		INSERT INTO accounts(id, name, balance) VALUES (1, 'Ada', 5000), (999, 'Victim', 99999);
		CREATE TABLE secrets (id INTEGER PRIMARY KEY AUTOINCREMENT, secret TEXT NOT NULL, tag TEXT NOT NULL);
		CREATE TABLE join_depts (id INTEGER PRIMARY KEY, code TEXT NOT NULL, name TEXT NOT NULL, delete_time DATETIME NULL);
		CREATE TABLE join_emps (id INTEGER PRIMARY KEY, dept_id INTEGER NOT NULL, dept_code TEXT NOT NULL, name TEXT NOT NULL, delete_time DATETIME NULL);
		INSERT INTO join_depts(id, code, name, delete_time) VALUES (1, 'ENG', 'Engineering', NULL);
		INSERT INTO join_emps(id, dept_id, dept_code, name, delete_time) VALUES (1, 1, 'ENG', 'Alice', NULL);
	`); err != nil {
		t.Fatal(err)
	}

	// #594: WITHOUT ROWID 雪花 ID 与 UUID 字符串主键
	orderMdl := NewModel(database, "orders_norowid")
	if err := orderMdl.On(ModelBeforeInsert, func(d map[string]any) bool {
		d["id"] = int64(9001)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	ord := &issue594SnowflakeOrderModel{Name: "book"}
	if err := orderMdl.Create(ord); err != nil || ord.ID != 9001 {
		t.Fatalf("SQLite WITHOUT ROWID 创建雪花主键失败: id=%d err=%v", ord.ID, err)
	}
	uuidMdl := NewModel(database, "uuid_items")
	if err := uuidMdl.On(ModelBeforeInsert, func(d map[string]any) bool {
		d["id"] = "uuid-sqlite-1"
		return true
	}); err != nil {
		t.Fatal(err)
	}
	uuidRec := &issue594StringUUIDModel{Name: "pen"}
	if err := uuidMdl.Create(uuidRec); err != nil || uuidRec.ID != "uuid-sqlite-1" {
		t.Fatalf("SQLite 创建 UUID 主键失败: id=%q err=%v", uuidRec.ID, err)
	}

	// #595: AutoTimestamp + SoftDelete 增量更新与恢复
	u := &issue595ValueTimeUser{Name: "Alice"}
	uMdl, err := NewModelFor(context.Background(), database, u)
	if err != nil {
		t.Fatal(err)
	}
	uMdl.Table("ts_users").AutoTimestamp(true).SoftDelete()
	if err := u.Save(); err != nil {
		t.Fatalf("SQLite time.Time 插入失败: %v", err)
	}
	u.Name = "Alice2"
	if err := u.Save(); err != nil {
		t.Fatalf("SQLite time.Time 增量更新失败: %v", err)
	}
	if err := u.Delete(); err != nil || u.DeleteTime == nil {
		t.Fatalf("SQLite time.Time 软删除失败: %v", err)
	}
	if err := u.Restore(); err != nil || u.DeleteTime != nil {
		t.Fatalf("SQLite time.Time 恢复失败: %v", err)
	}

	// #596: 空外键不泄露 orphan_secret
	uTable := NewModel(database, "accounts")
	pTable := NewModel(database, "posts")
	if leaked, err := uTable.HasMany(pTable, "user_id", nil); err != nil || len(leaked) != 0 {
		t.Fatalf("SQLite 空关联键泄露无主数据: rows=%#v err=%v", leaked, err)
	}

	// #597: 部分列投影后 tableModel.Save(&acc) 保护未加载列 balance=5000
	accMdl := NewModel(database, "accounts")
	var acc issue597PartialAccount
	if found, err := accMdl.Field("id, name").Where("id", 1).Find(&acc); err != nil || !found {
		t.Fatalf("SQLite 部分投影查询失败: %v", err)
	}
	acc.Name = "Ada-Updated"
	if err := accMdl.Save(&acc); err != nil {
		t.Fatalf("SQLite accMdl.Save(&acc) 失败: %v", err)
	}
	var balance int64
	if err := raw.QueryRow("SELECT balance FROM accounts WHERE id = 1").Scan(&balance); err != nil || balance != 5000 {
		t.Fatalf("SQLite balance 被意外清零: balance=%d err=%v", balance, err)
	}
	acc.ID = 999
	if err := accMdl.Save(&acc); !errors.Is(err, ErrModelIdentityChanged) {
		t.Fatalf("SQLite 篡改主键未被拦截: %v", err)
	}

	// #598: 指针接收者 driver.Valuer 值字段写入与 SoftDelete Join
	secMdl := NewModel(database, "secrets")
	if err := secMdl.Create(&issue598SecretItem{
		Secret: issue598SecretText{raw: []byte("top-secret")},
		Tag:    issue598ExportedPtrValuer{Label: "prod"},
	}); err != nil {
		t.Fatalf("SQLite 指针接收者 Valuer 写入失败: %v", err)
	}
	empJoinMdl := NewModel(database, "join_emps").SoftDelete()
	joinedRows, err := empJoinMdl.Join("join_depts", "join_emps.dept_id = join_depts.id").SelectMaps()
	if err != nil || len(joinedRows) != 1 {
		t.Fatalf("SQLite SoftDelete Join 查询失败: rows=%#v err=%v", joinedRows, err)
	}
}
