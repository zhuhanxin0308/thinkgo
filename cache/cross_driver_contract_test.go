package cache

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
	dbCacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver/database"
	"github.com/zhuhanxin0308/thinkgo/v3/db"
	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
)

// memSQLDriver 为跨驱动契约测试在 CGO_ENABLED=0 下提供支持事务的纯 Go 内存 SQL 驱动。
type memSQLDriver struct {
	mu        sync.Mutex
	committed map[string]memSQLRow
	staged    map[string]*memSQLRow
	inTx      bool
}

type memSQLRow struct {
	key    string
	value  string
	expiry int64
}

type memSQLTx struct{ d *memSQLDriver }

func (tx *memSQLTx) Commit() error {
	tx.d.mu.Lock()
	defer tx.d.mu.Unlock()
	for k, v := range tx.d.staged {
		if v == nil {
			delete(tx.d.committed, k)
		} else {
			tx.d.committed[k] = *v
		}
	}
	tx.d.staged = nil
	tx.d.inTx = false
	return nil
}

func (tx *memSQLTx) Rollback() error {
	tx.d.mu.Lock()
	defer tx.d.mu.Unlock()
	tx.d.staged = nil
	tx.d.inTx = false
	return nil
}

type memSQLStmt struct {
	d     *memSQLDriver
	query string
}

func (s *memSQLStmt) Close() error  { return nil }
func (s *memSQLStmt) NumInput() int { return -1 }
func (s *memSQLStmt) Exec(args []driver.Value) (driver.Result, error) {
	named := make([]driver.NamedValue, len(args))
	for i, v := range args {
		named[i] = driver.NamedValue{Ordinal: i + 1, Value: v}
	}
	return s.d.ExecContext(context.Background(), s.query, named)
}
func (s *memSQLStmt) Query(args []driver.Value) (driver.Rows, error) {
	named := make([]driver.NamedValue, len(args))
	for i, v := range args {
		named[i] = driver.NamedValue{Ordinal: i + 1, Value: v}
	}
	return s.d.QueryContext(context.Background(), s.query, named)
}
func (s *memSQLStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	return s.d.ExecContext(ctx, s.query, args)
}
func (s *memSQLStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	return s.d.QueryContext(ctx, s.query, args)
}

type memSQLRows struct {
	cols []string
	rows [][]driver.Value
	idx  int
}

func (r *memSQLRows) Columns() []string { return r.cols }
func (r *memSQLRows) Close() error      { return nil }
func (r *memSQLRows) Next(dest []driver.Value) error {
	if r.idx >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.idx])
	r.idx++
	return nil
}

func (d *memSQLDriver) getLocked(key string) (memSQLRow, bool) {
	if d.inTx && d.staged != nil {
		if row, ok := d.staged[key]; ok {
			if row == nil {
				return memSQLRow{}, false
			}
			return *row, true
		}
	}
	row, ok := d.committed[key]
	return row, ok
}

func (d *memSQLDriver) setLocked(row memSQLRow) {
	if d.inTx {
		cp := row
		d.staged[row.key] = &cp
		return
	}
	d.committed[row.key] = row
}

func (d *memSQLDriver) delLocked(key string) {
	if d.inTx {
		d.staged[key] = nil
		return
	}
	delete(d.committed, key)
}

func (d *memSQLDriver) allLocked() map[string]memSQLRow {
	merged := make(map[string]memSQLRow, len(d.committed))
	for k, v := range d.committed {
		merged[k] = v
	}
	if d.inTx {
		for k, v := range d.staged {
			if v == nil {
				delete(merged, k)
			} else {
				merged[k] = *v
			}
		}
	}
	return merged
}

func (d *memSQLDriver) Connect(context.Context) (driver.Conn, error) { return d, nil }
func (d *memSQLDriver) Driver() driver.Driver                        { return d }
func (d *memSQLDriver) Open(string) (driver.Conn, error)             { return d, nil }
func (d *memSQLDriver) Close() error                                 { return nil }
func (d *memSQLDriver) Prepare(query string) (driver.Stmt, error) {
	return &memSQLStmt{d: d, query: query}, nil
}
func (d *memSQLDriver) Begin() (driver.Tx, error) {
	return d.BeginTx(context.Background(), driver.TxOptions{})
}
func (d *memSQLDriver) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.inTx = true
	d.staged = make(map[string]*memSQLRow)
	return &memSQLTx{d: d}, nil
}

func (d *memSQLDriver) QueryContext(_ context.Context, _ string, args []driver.NamedValue) (driver.Rows, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(args) == 0 {
		return &memSQLRows{}, nil
	}
	row, ok := d.getLocked(fmt.Sprint(args[0].Value))
	if !ok {
		return &memSQLRows{}, nil
	}
	return &memSQLRows{
		cols: []string{"key", "value", "expiry"},
		rows: [][]driver.Value{{row.key, row.value, row.expiry}},
	}, nil
}

func (d *memSQLDriver) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	upper := strings.ToUpper(strings.TrimSpace(query))
	switch {
	case strings.HasPrefix(upper, "CREATE"):
		return driver.RowsAffected(0), nil
	case strings.HasPrefix(upper, "INSERT"):
		var row memSQLRow
		if strings.Index(upper, "EXPIRY") < strings.Index(upper, "KEY") {
			exp, _ := strconv.ParseInt(fmt.Sprint(args[0].Value), 10, 64)
			row = memSQLRow{expiry: exp, key: fmt.Sprint(args[1].Value), value: fmt.Sprint(args[2].Value)}
		} else {
			exp, _ := strconv.ParseInt(fmt.Sprint(args[2].Value), 10, 64)
			row = memSQLRow{key: fmt.Sprint(args[0].Value), value: fmt.Sprint(args[1].Value), expiry: exp}
		}
		if _, exists := d.getLocked(row.key); exists {
			return nil, errors.New("duplicate key")
		}
		d.setLocked(row)
		return driver.RowsAffected(1), nil
	case strings.HasPrefix(upper, "DELETE"):
		if strings.Contains(upper, "LIKE") && len(args) == 2 {
			prefix := strings.TrimSuffix(fmt.Sprint(args[0].Value), "%")
			bound := fmt.Sprint(args[1].Value)
			var deleted int64
			for k := range d.allLocked() {
				if strings.HasPrefix(k, prefix) && k < bound {
					d.delLocked(k)
					deleted++
				}
			}
			return driver.RowsAffected(deleted), nil
		}
		key := fmt.Sprint(args[0].Value)
		row, exists := d.getLocked(key)
		if !exists {
			return driver.RowsAffected(0), nil
		}
		if len(args) == 2 && strings.Contains(upper, "EXPIRY") {
			wantExp, _ := strconv.ParseInt(fmt.Sprint(args[1].Value), 10, 64)
			if row.expiry != wantExp {
				return driver.RowsAffected(0), nil
			}
		} else if len(args) == 3 {
			arg1 := fmt.Sprint(args[1].Value)
			arg2, _ := strconv.ParseInt(fmt.Sprint(args[2].Value), 10, 64)
			if strings.Contains(upper, ">") {
				if row.value != arg1 || row.expiry <= arg2 {
					return driver.RowsAffected(0), nil
				}
			} else {
				if row.value != arg1 || row.expiry != arg2 {
					return driver.RowsAffected(0), nil
				}
			}
		}
		d.delLocked(key)
		return driver.RowsAffected(1), nil
	case strings.HasPrefix(upper, "UPDATE"):
		// 1) SET expiry = ? WHERE key = ? AND value = ? AND expiry > ? (RenewLock)
		// 2) SET value = ? WHERE key = ? AND value = ? AND expiry > ? (Lease guard)
		if len(args) == 4 && strings.Contains(upper, ">") {
			setPart, _, _ := strings.Cut(upper, "WHERE")
			key := fmt.Sprint(args[1].Value)
			wantVal := fmt.Sprint(args[2].Value)
			now, _ := strconv.ParseInt(fmt.Sprint(args[3].Value), 10, 64)
			row, exists := d.getLocked(key)
			if !exists || row.value != wantVal || row.expiry <= now {
				return driver.RowsAffected(0), nil
			}
			if strings.Contains(setPart, "EXPIRY") {
				exp, _ := strconv.ParseInt(fmt.Sprint(args[0].Value), 10, 64)
				row.expiry = exp
			} else {
				row.value = fmt.Sprint(args[0].Value)
			}
			d.setLocked(row)
			return driver.RowsAffected(1), nil
		}
		// 3) SET expiry = ?, key = ?, value = ? WHERE key = ? AND expiry <= ? (Expired lock takeover)
		if len(args) == 5 && strings.Contains(upper, "<=") {
			exp, _ := strconv.ParseInt(fmt.Sprint(args[0].Value), 10, 64)
			key := fmt.Sprint(args[3].Value)
			val := fmt.Sprint(args[2].Value)
			now, _ := strconv.ParseInt(fmt.Sprint(args[4].Value), 10, 64)
			row, exists := d.getLocked(key)
			if !exists || row.expiry > now {
				return driver.RowsAffected(0), nil
			}
			row.expiry = exp
			row.value = val
			d.setLocked(row)
			return driver.RowsAffected(1), nil
		}
		// 4) CAS update: SET expiry = ?, value = ? WHERE key = ? AND value = ? AND expiry = ?
		if len(args) == 5 {
			exp, _ := strconv.ParseInt(fmt.Sprint(args[0].Value), 10, 64)
			val := fmt.Sprint(args[1].Value)
			key := fmt.Sprint(args[2].Value)
			wantVal := fmt.Sprint(args[3].Value)
			wantExp, _ := strconv.ParseInt(fmt.Sprint(args[4].Value), 10, 64)
			row, exists := d.getLocked(key)
			if !exists || row.value != wantVal || row.expiry != wantExp {
				return driver.RowsAffected(0), nil
			}
			row.expiry = exp
			row.value = val
			d.setLocked(row)
			return driver.RowsAffected(1), nil
		}
		// 5) Generation CAS: SET value = ? WHERE key = ? AND value = ?
		if len(args) == 3 {
			newVal := fmt.Sprint(args[0].Value)
			key := fmt.Sprint(args[1].Value)
			wantVal := fmt.Sprint(args[2].Value)
			row, exists := d.getLocked(key)
			if !exists || row.value != wantVal {
				return driver.RowsAffected(0), nil
			}
			row.value = newVal
			d.setLocked(row)
			return driver.RowsAffected(1), nil
		}
		// 6) Lease owner restore: SET value = ? WHERE key = ?
		if len(args) == 2 {
			newVal := fmt.Sprint(args[0].Value)
			key := fmt.Sprint(args[1].Value)
			row, exists := d.getLocked(key)
			if !exists {
				return driver.RowsAffected(0), nil
			}
			row.value = newVal
			d.setLocked(row)
			return driver.RowsAffected(1), nil
		}
		// 7) Standard upsert update: SET expiry = ?, key = ?, value = ? WHERE key = ?
		if len(args) == 4 {
			exp, _ := strconv.ParseInt(fmt.Sprint(args[0].Value), 10, 64)
			val := fmt.Sprint(args[2].Value)
			key := fmt.Sprint(args[3].Value)
			row, exists := d.getLocked(key)
			if !exists {
				return driver.RowsAffected(0), nil
			}
			row.expiry = exp
			row.value = val
			d.setLocked(row)
			return driver.RowsAffected(1), nil
		}
	}
	return driver.RowsAffected(0), nil
}

func newPureGoDBCacheDriver(t *testing.T) Driver {
	t.Helper()
	mock := &memSQLDriver{committed: make(map[string]memSQLRow)}
	pool := sql.OpenDB(mock)
	pool.SetMaxOpenConns(1)
	conn := db.NewSQLConnection(pool, &builder.Sqlite{})
	t.Cleanup(func() { _ = conn.Close() })
	backend, err := dbCacheDriver.NewDB(conn, "think_cache")
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.EnsureTable(); err != nil {
		t.Fatal(err)
	}
	return backend
}

// TestIssue601CrossDriverContractConsistency 验证 Memory/File/DB/Redis 四大驱动在大整数、亚毫秒锁、长键和原子操作上的契约一致性。
func TestIssue601CrossDriverContractConsistency(t *testing.T) {
	drivers := []struct {
		name    string
		factory func(*testing.T) Driver
	}{
		{"memory", func(*testing.T) Driver { return cacheDriver.NewMemory() }},
		{"file", func(t *testing.T) Driver {
			d, err := cacheDriver.NewFile(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			return d
		}},
		{"database", newPureGoDBCacheDriver},
		{"redis", atomicOrderRedis},
	}

	for _, drv := range drivers {
		t.Run(drv.name, func(t *testing.T) {
			store := NewCache(nil, drv.factory(t))

			// 1. int64 / uint64 / 宽整数 json.Number 无损往返（类型与数值均保持精确）
			preciseValues := []struct {
				key  string
				want interface{}
			}{
				{"max_int64", int64(math.MaxInt64)},
				{"min_int64", int64(math.MinInt64)},
				{"max_uint64", uint64(math.MaxUint64)},
				{"num_i64", json.Number("9223372036854775807")},
				{"num_u64", json.Number("18446744073709551615")},
				{"num_wide", json.Number("123456789012345678901234567890")},
			}
			for _, item := range preciseValues {
				if err := store.Set(item.key, item.want, time.Minute); err != nil {
					t.Fatalf("Set(%s) failed: %v", item.key, err)
				}
				got, found, err := store.Get(item.key)
				if err != nil || !found || got != item.want {
					t.Fatalf("Get(%s) mismatch: got=%#v (%T) found=%t err=%v, want=%#v (%T)", item.key, got, got, found, err, item.want, item.want)
				}
			}

			// 2. SetMany / GetMany 精确整数一致性
			batchInput := map[string]interface{}{
				"batch_i64":  int64(math.MaxInt64 - 3),
				"batch_u64":  uint64(math.MaxUint64 - 5),
				"batch_wide": json.Number("-9223372036854775809"),
			}
			if err := store.SetMany(batchInput, time.Minute); err != nil {
				t.Fatalf("SetMany failed: %v", err)
			}
			batchOut, err := store.GetMany([]string{"batch_i64", "batch_u64", "batch_wide"})
			if err != nil {
				t.Fatalf("GetMany failed: %v", err)
			}
			for k, want := range batchInput {
				if got := batchOut[k]; got != want {
					t.Fatalf("GetMany[%s] mismatch: got=%#v (%T), want=%#v (%T)", k, got, got, want, want)
				}
			}

			// 3. Inc / Dec 与 Update 保持 int64 精度
			if val, err := store.Inc("counter_601", 10); err != nil || val != 10 {
				t.Fatalf("Inc failed: val=%d err=%v", val, err)
			}
			if val, err := store.Dec("counter_601", 3); err != nil || val != 7 {
				t.Fatalf("Dec failed: val=%d err=%v", val, err)
			}
			if got, found, err := store.Get("counter_601"); err != nil || !found || got != int64(7) {
				t.Fatalf("counter Get type/value mismatch: got=%#v (%T) found=%t err=%v", got, got, found, err)
			}
			if err := store.Update("atomic_601", time.Minute, func(cur interface{}, found bool) (interface{}, bool, error) {
				if found {
					t.Fatalf("unexpected hit: %#v", cur)
				}
				return int64(math.MaxInt64 - 1), false, nil
			}); err != nil {
				t.Fatalf("Update initial failed: %v", err)
			}
			if err := store.Update("atomic_601", time.Minute, func(cur interface{}, found bool) (interface{}, bool, error) {
				if !found || cur != int64(math.MaxInt64-1) {
					t.Fatalf("Update read lost precision: found=%t cur=%#v (%T)", found, cur, cur)
				}
				return int64(math.MaxInt64), false, nil
			}); err != nil {
				t.Fatalf("Update increment failed: %v", err)
			}

			// 4. RememberWithLock 保留高精度整数
			remembered, err := store.RememberWithLock("remember_lock_601", time.Minute, time.Second, func() (interface{}, error) {
				return int64(math.MaxInt64), nil
			})
			if err != nil || remembered != int64(math.MaxInt64) {
				t.Fatalf("RememberWithLock failed: val=%#v err=%v", remembered, err)
			}
			if got, found, err := store.Get("remember_lock_601"); err != nil || !found || got != int64(math.MaxInt64) {
				t.Fatalf("RememberWithLock persisted value mismatch: got=%#v (%T) found=%t err=%v", got, got, found, err)
			}

			// 5. 300 字节与 1024 字节逻辑键（以及命名空间包装下的 1024 字节键）全部支持
			for _, keyLen := range []int{300, 1024} {
				longKey := strings.Repeat("k", keyLen)
				if err := store.Set(longKey, int64(keyLen), time.Minute); err != nil {
					t.Fatalf("Set(%d-byte key) failed: %v", keyLen, err)
				}
				if got, found, err := store.Get(longKey); err != nil || !found || got != int64(keyLen) {
					t.Fatalf("Get(%d-byte key) mismatch: got=%#v found=%t err=%v", keyLen, got, found, err)
				}
			}
			nsDrv, err := NewNamespaceDriver(drv.factory(t), "tenant-ns:", "")
			if err != nil {
				t.Fatal(err)
			}
			nsCache := NewCache(nil, nsDrv)
			maxKey := strings.Repeat("m", 1024)
			if err := nsCache.Set(maxKey, "ns-ok", time.Minute); err != nil {
				t.Fatalf("namespaced 1024-byte key Set failed: %v", err)
			}
			if got, found, err := nsCache.Get(maxKey); err != nil || !found || got != "ns-ok" {
				t.Fatalf("namespaced 1024-byte key Get mismatch: got=%#v found=%t err=%v", got, found, err)
			}

			// 6. 亚毫秒锁互斥性：在首次获锁与立即竞争未跨越 1ms 窗口时绝不允许双持有者同时获锁
			mutualVerified := false
			for attempt := 0; attempt < 20; attempt++ {
				lockName := fmt.Sprintf("subms_job_%d", attempt)
				l1, err := store.Lock(lockName, 800*time.Microsecond)
				if err != nil {
					t.Fatal(err)
				}
				l2, err := store.Lock(lockName, time.Second)
				if err != nil {
					t.Fatal(err)
				}
				start := time.Now()
				ok1, err1 := l1.Acquire()
				ok2, err2 := l2.Acquire()
				elapsed := time.Since(start)
				if err1 != nil || err2 != nil {
					t.Fatalf("Lock Acquire error: err1=%v err2=%v", err1, err2)
				}
				if !ok1 {
					t.Fatal("first sub-ms lock Acquire should succeed")
				}
				if elapsed < 800*time.Microsecond {
					if ok2 {
						t.Fatalf("dual lock holders acquired simultaneously within %v!", elapsed)
					}
					mutualVerified = true
					_, _ = l1.Release()
					break
				}
				_, _ = l2.Release()
			}
			if !mutualVerified {
				t.Log("host scheduling exceeded 800µs on all attempts; dbExpiryMillis unit tests still guarantee ceiling")
			}
		})
	}
}
