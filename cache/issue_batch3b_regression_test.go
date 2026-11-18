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

	"github.com/alicebob/miniredis/v2"
	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
	dbCacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver/database"
	redisDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver/redis"
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

// TestIssue602TaggedFlushPreservesLiveValueOnForwardGhost 验证 TaggedCache.Flush 在反向证明缺失时绝不推进存活业务键的失效水位。
func TestIssue602TaggedFlushPreservesLiveValueOnForwardGhost(t *testing.T) {
	backends := map[string]func(*testing.T) Driver{
		"memory": func(*testing.T) Driver { return cacheDriver.NewMemory() },
		"file": func(t *testing.T) Driver {
			d, err := cacheDriver.NewFile(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			return d
		},
		"redis": atomicOrderRedis,
	}
	for name, factory := range backends {
		t.Run(name, func(t *testing.T) {
			raw := factory(t)
			ghostCache := NewCache(nil, raw)

			if err := ghostCache.Set("live_key", "important_data", 0); err != nil {
				t.Fatal(err)
			}
			gt1, err := ghostCache.Tag("t1")
			if err != nil {
				t.Fatal(err)
			}
			if err := gt1.Set("tagged_victim", "delete_me", 0); err != nil {
				t.Fatal(err)
			}
			// 注入孤立正向成员：包含存活无标签键 "live_key"、真实标签键 "tagged_victim" 与已不存在的幽灵键 "dead_ghost"
			if err := raw.Set(ghostCache.tagMetaKey("t1"), []string{"live_key", "tagged_victim", "dead_ghost"}, 0); err != nil {
				t.Fatal(err)
			}

			if err := gt1.Flush(); err != nil {
				t.Fatalf("TaggedCache.Flush failed: %v", err)
			}

			val, found, err := ghostCache.Get("live_key")
			if err != nil || !found || val != "important_data" {
				t.Fatalf("forwardGhost killed live business key! found=%t val=%#v err=%v", found, val, err)
			}
			if _, found, err := ghostCache.Get("tagged_victim"); err != nil || found {
				t.Fatalf("valid tagged key was not flushed: found=%t err=%v", found, err)
			}
			if _, metaFound, err := raw.Get(ghostCache.tagMetaKey("t1")); err != nil || metaFound {
				t.Fatalf("orphan forward tag metadata was not cleaned: found=%t err=%v", metaFound, err)
			}
		})
	}
}

// TestIssue602TagFlushTwoLargeTagsAndLongRunningForgetPruneCompletedScopes 验证两个 8200 键合法标签联合清理与单桶长期 Forget 均不溢出。
func TestIssue602TagFlushTwoLargeTagsAndLongRunningForgetPruneCompletedScopes(t *testing.T) {
	// 1. 两个各含 8,200 键的合法标签（共 16,400 > 旧上限 16,384）联合 Flush 100% 成功
	memCap := cacheDriver.NewMemory()
	capCache := NewCache(nil, memCap)
	tagA, err := capCache.Tag("tagA")
	if err != nil {
		t.Fatal(err)
	}
	tagB, err := capCache.Tag("tagB")
	if err != nil {
		t.Fatal(err)
	}
	// 直接使用批量标签绑定与写入加速构建 2 × 8,200 个真实标签键（与 TaggedCache.Set 元数据结构 100% 一致）
	membersA := make([]string, 0, 8200)
	membersB := make([]string, 0, 8200)
	for i := 0; i < 8200; i++ {
		ka := fmt.Sprintf("a_key_%d", i)
		kb := fmt.Sprintf("b_key_%d", i)
		membersA = append(membersA, ka)
		membersB = append(membersB, kb)
		if err := capCache.Set(ka, i, time.Hour); err != nil {
			t.Fatal(err)
		}
		if err := memCap.Set(capCache.tagReverseKey(ka), []string{"tagA"}, 0); err != nil {
			t.Fatal(err)
		}
		if err := capCache.Set(kb, i, time.Hour); err != nil {
			t.Fatal(err)
		}
		if err := memCap.Set(capCache.tagReverseKey(kb), []string{"tagB"}, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := memCap.Set(capCache.tagMetaKey("tagA"), membersA, 0); err != nil {
		t.Fatal(err)
	}
	if err := memCap.Set(capCache.tagMetaKey("tagB"), membersB, 0); err != nil {
		t.Fatal(err)
	}
	_ = tagA
	_ = tagB

	tcBoth, err := capCache.Tag("tagA", "tagB")
	if err != nil {
		t.Fatal(err)
	}
	if err := tcBoth.Flush(); err != nil {
		t.Fatalf("joint Flush of 16,400 legal tagged keys overflowed: %v", err)
	}
	for _, sample := range []string{"a_key_0", "a_key_8199", "b_key_0", "b_key_8199"} {
		if _, found, err := capCache.Get(sample); err != nil || found {
			t.Fatalf("sample key %s survived joint Flush: found=%t err=%v", sample, found, err)
		}
	}

	// 2. 单桶长期累计 Forget 超过 maxCacheInvalidationScopes（2048 + 300 = 2348 个同桶键）自动裁剪已完成旧水位
	memLong := cacheDriver.NewMemory()
	longCache := NewCache(nil, memLong)
	const targetBucket = 0
	const totalSameBucketKeys = maxCacheInvalidationScopes + 300
	collected := 0
	for seq := 0; collected < totalSameBucketKeys; seq++ {
		candidate := fmt.Sprintf("ephemeral_%d", seq)
		if keyInvalidationBucket(candidate) != targetBucket {
			continue
		}
		collected++
		if err := longCache.Set(candidate, seq, time.Minute); err != nil {
			t.Fatalf("Set(%s) at #%d failed: %v", candidate, collected, err)
		}
		if err := longCache.Forget(candidate); err != nil {
			t.Fatalf("long-running Forget(%s) at #%d leaked bucket capacity: %v", candidate, collected, err)
		}
		if _, found, err := longCache.Get(candidate); err != nil || found {
			t.Fatalf("forgotten key %s remained visible: found=%t err=%v", candidate, found, err)
		}
	}
	rawBucket, bucketFound, err := memLong.Get(longCache.keyInvalidationBucketKey(targetBucket))
	if err != nil || !bucketFound {
		t.Fatalf("expected bucket %d metadata, found=%t err=%v", targetBucket, bucketFound, err)
	}
	if entries, ok := rawBucket.(map[string]interface{}); !ok || len(entries) > maxCacheInvalidationScopes {
		t.Fatalf("bucket %d exceeded maxCacheInvalidationScopes: len=%d", targetBucket, len(entries))
	}
}

// TestIssue602SetManyRollbackNeverResurrectsInvalidatedValues 验证 SetMany 回滚绝不会把已被失效水位屏蔽的旧值赋新代际写活。
func TestIssue602SetManyRollbackNeverResurrectsInvalidatedValues(t *testing.T) {
	backends := map[string]func(*testing.T) Driver{
		"memory": func(*testing.T) Driver { return cacheDriver.NewMemory() },
		"file": func(t *testing.T) Driver {
			d, err := cacheDriver.NewFile(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			return d
		},
		"redis": atomicOrderRedis,
	}
	for name, factory := range backends {
		t.Run(name, func(t *testing.T) {
			raw := factory(t)
			faultErr := errors.New("simulated batch write fault")
			faultDriver := &batchRollbackFaultDriver{Driver: raw, failure: faultErr}
			fastCache := NewCache(nil, raw)
			rollCache := NewCache(nil, faultDriver)

			// 场景 1：Forget("a") 推进精确失效水位后，底层残留未信封旧值 (origin=0)
			if err := fastCache.Forget("a"); err != nil {
				t.Fatal(err)
			}
			if err := raw.Set("a", "dead_legacy", time.Hour); err != nil {
				t.Fatal(err)
			}
			if _, found, err := fastCache.Get("a"); err != nil || found {
				t.Fatalf("precondition failed: dead_legacy should be masked before SetMany, got found=%t err=%v", found, err)
			}

			// 场景 2：已被 ClearPrefix("stale:") 屏蔽的旧代际信封值
			if err := fastCache.Set("stale:item", "dead_enveloped", time.Hour); err != nil {
				t.Fatal(err)
			}
			staleRaw, _, _ := raw.Get("stale:item")
			if err := fastCache.ClearPrefix("stale:"); err != nil {
				t.Fatal(err)
			}
			// 重新注入旧代际信封物理残留
			if err := raw.Set("stale:item", staleRaw, time.Hour); err != nil {
				t.Fatal(err)
			}
			if _, found, err := fastCache.Get("stale:item"); err != nil || found {
				t.Fatalf("precondition failed: stale:item should be masked before SetMany, got found=%t err=%v", found, err)
			}

			// 场景 3：正常存活键 "healthy" 在回滚后仍应恢复原值，而并发在批次中途被 Forget 的键 "mid_forget" 也不得复活
			if err := fastCache.Set("healthy", "alive_before", time.Hour); err != nil {
				t.Fatal(err)
			}
			if err := fastCache.Set("mid_forget", "forgotten_during_batch", time.Hour); err != nil {
				t.Fatal(err)
			}
			faultDriver.beforeFail = func() {
				if err := fastCache.Forget("mid_forget"); err != nil {
					t.Errorf("concurrent Forget failed: %v", err)
				}
			}

			err := rollCache.SetMany(map[string]interface{}{
				"a":          "new_a",
				"healthy":    "new_healthy",
				"mid_forget": "new_mid",
				"stale:item": "new_stale",
				"z-fail":     "trigger",
			}, time.Hour)
			if !errors.Is(err, faultErr) {
				t.Fatalf("expected faultErr, got %v", err)
			}

			for _, deadKey := range []string{"a", "stale:item", "mid_forget"} {
				if val, found, getErr := fastCache.Get(deadKey); getErr != nil || found {
					t.Fatalf("SetMany rollback resurrected invalidated key %q! val=%#v found=%t err=%v", deadKey, val, found, getErr)
				}
			}
			if val, found, getErr := fastCache.Get("healthy"); getErr != nil || !found || val != "alive_before" {
				t.Fatalf("SetMany rollback failed to restore healthy key: val=%#v found=%t err=%v", val, found, getErr)
			}
		})
	}

	// 同时验证 Issue #602 最小复现中的 make(chan int) 序列化错误在 Memory 驱动下同样不复活死数据
	memRoll := cacheDriver.NewMemory()
	rollCache := NewCache(nil, memRoll)
	_ = rollCache.Forget("a")
	_ = memRoll.Set("a", "dead_legacy", time.Hour)
	if err := rollCache.SetMany(map[string]interface{}{"a": "new", "b": make(chan int)}, time.Hour); err == nil {
		t.Fatal("expected SetMany with chan to fail when persisted or validated")
	}
	if val, found, err := rollCache.Get("a"); err != nil || found {
		t.Fatalf("SetMany with chan resurrected dead_legacy: val=%#v found=%t err=%v", val, found, err)
	}
}

// TestIssue603RedisClearAllowsReservedSubstringsAndNestedNamespaceLocks 验证业务键含保留子串及嵌套 NamespaceDriver 持锁时 Flush/ClearPrefix 正常工作。
func TestIssue603RedisClearAllowsReservedSubstringsAndNestedNamespaceLocks(t *testing.T) {
	server := miniredis.RunT(t)
	rds, err := redisDriver.NewRedis(map[string]interface{}{
		"host":           server.Host(),
		"port":           server.Server().Addr().Port,
		"prefix":         "",
		"allow_flush_db": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rds.Close() })

	// 1. 合法业务键中间包含 "__thinkgo_lock__:" 与 "__thinkgo_cache_fence__:" 不阻塞 ClearPrefix / Flush
	rdsCache := NewCache(nil, rds)
	if err := rdsCache.Set("doc:__thinkgo_lock__:intro", "ok", time.Hour); err != nil {
		t.Fatalf("Set business key with lock substring failed: %v", err)
	}
	if err := rdsCache.Set("doc:__thinkgo_cache_fence__:guide", "ok2", time.Hour); err != nil {
		t.Fatalf("Set business key with fence substring failed: %v", err)
	}
	activeLock, err := rdsCache.Lock("deploy-job", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := activeLock.Acquire(); err != nil || !ok {
		t.Fatalf("Acquire active lock failed: ok=%t err=%v", ok, err)
	}

	if err := rdsCache.ClearPrefix("doc:"); err != nil {
		t.Fatalf("ClearPrefix(\"doc:\") blocked by substring false positive: %v", err)
	}
	if _, found, err := rdsCache.Get("doc:__thinkgo_lock__:intro"); err != nil || found {
		t.Fatalf("business key survived ClearPrefix: found=%t err=%v", found, err)
	}

	if err := rdsCache.Set("article:__thinkgo_lock__:v2", "body", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := rdsCache.Flush(); err != nil {
		t.Fatalf("Flush() blocked by substring false positive: %v", err)
	}
	if _, found, err := rdsCache.Get("article:__thinkgo_lock__:v2"); err != nil || found {
		t.Fatalf("business key survived Flush: found=%t err=%v", found, err)
	}
	// 确认真实分布式锁依然被保护未误删
	competitorLock, _ := rdsCache.Lock("deploy-job", time.Minute)
	if ok, err := competitorLock.Acquire(); err != nil || ok {
		t.Fatalf("Flush() broke active lock! ok=%t err=%v", ok, err)
	}
	_, _ = activeLock.Release()

	// 2. 嵌套两层 NamespaceDriver 且内层持锁时，外层 Flush() 正常清理业务数据并保留内层锁
	outerDriver, err := NewNamespaceDriver(rds, "outer:", "")
	if err != nil {
		t.Fatal(err)
	}
	innerDriver, err := NewNamespaceDriver(outerDriver, "inner:", "")
	if err != nil {
		t.Fatal(err)
	}
	outerCache := NewCache(nil, outerDriver)
	innerCache := NewCache(nil, innerDriver)

	if err := outerCache.Set("outer_k", "v1", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := innerCache.Set("inner_k", "v2", time.Hour); err != nil {
		t.Fatal(err)
	}
	innerLock, err := innerCache.Lock("nested-lock", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := innerLock.Acquire(); err != nil || !ok {
		t.Fatalf("inner lock Acquire failed: ok=%t err=%v", ok, err)
	}
	if err := outerCache.Flush(); err != nil {
		t.Fatalf("outerCache.Flush() failed when nested NamespaceDriver held active lock: %v", err)
	}
	if _, found, err := innerCache.Get("inner_k"); err != nil || found {
		t.Fatalf("inner business key survived outer Flush: found=%t err=%v", found, err)
	}
	innerCompetitor, _ := innerCache.Lock("nested-lock", time.Minute)
	if ok, err := innerCompetitor.Acquire(); err != nil || ok {
		t.Fatalf("outer Flush deleted nested active lock! ok=%t err=%v", ok, err)
	}
	_, _ = innerLock.Release()
}

// TestIssue603RememberContextDecouplesLeaderCancelAndClonesMutableValues 验证 Leader 取消不连坐健康 Follower，且并发调用获得独立可变副本。
func TestIssue603RememberContextDecouplesLeaderCancelAndClonesMutableValues(t *testing.T) {
	store := NewCache(nil, cacheDriver.NewMemory())

	// 1. Leader(ctx1) 超时取消不杀死携带 Background() 的健康 Follower，且计算结果正常落盘
	entered := make(chan struct{})
	proceed := make(chan struct{})
	ctx1, cancel1 := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	var g1Err, g2Err error
	var g2Val interface{}

	wg.Add(2)
	go func() {
		defer wg.Done()
		_, g1Err = store.RememberContext(ctx1, "shared_flight", time.Minute, func() (interface{}, error) {
			close(entered)
			<-proceed
			return "computed_ok", nil
		})
	}()
	<-entered

	go func() {
		defer wg.Done()
		g2Val, g2Err = store.RememberContext(context.Background(), "shared_flight", time.Minute, func() (interface{}, error) {
			t.Error("Follower should join existing flight rather than invoking second callback")
			return "unexpected", nil
		})
	}()

	// 等待 G2 进入 waiters 队列后再取消 Leader ctx1
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		store.state.rememberMu.Lock()
		c := store.state.remembering["shared_flight"]
		waiters := 0
		if c != nil {
			waiters = c.waiters
		}
		store.state.rememberMu.Unlock()
		if waiters == 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}

	cancel1()
	close(proceed)
	wg.Wait()

	if !errors.Is(g1Err, context.Canceled) {
		t.Fatalf("expected canceled Leader to get context.Canceled, got %v", g1Err)
	}
	if g2Err != nil || g2Val != "computed_ok" {
		t.Fatalf("healthy Follower was killed by Leader cancellation! val=%#v err=%v", g2Val, g2Err)
	}
	if cached, found, err := store.Get("shared_flight"); err != nil || !found || cached != "computed_ok" {
		t.Fatalf("cache was not populated when healthy Follower remained: val=%#v found=%t err=%v", cached, found, err)
	}

	// 2. 所有等待者全部取消时，sharedCtx 自动传播取消并摘除挂起批次
	allEntered := make(chan struct{})
	sharedCanceled := make(chan error, 1)
	cA, cancelA := context.WithCancel(context.Background())
	cB, cancelB := context.WithCancel(context.Background())
	var wgCancel sync.WaitGroup
	wgCancel.Add(2)
	go func() {
		defer wgCancel.Done()
		_, _ = store.state.doRememberContext(cA, "all_cancel", func(sharedCtx context.Context) (interface{}, error) {
			close(allEntered)
			<-sharedCtx.Done()
			sharedCanceled <- sharedCtx.Err()
			return nil, sharedCtx.Err()
		})
	}()
	<-allEntered
	go func() {
		defer wgCancel.Done()
		_, _ = store.state.doRememberContext(cB, "all_cancel", func(context.Context) (interface{}, error) {
			return "unused", nil
		})
	}()
	for time.Now().Before(time.Now().Add(time.Second)) {
		store.state.rememberMu.Lock()
		c := store.state.remembering["all_cancel"]
		w := 0
		if c != nil {
			w = c.waiters
		}
		store.state.rememberMu.Unlock()
		if w == 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancelA()
	cancelB()
	wgCancel.Wait()
	select {
	case err := <-sharedCanceled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected sharedCtx context.Canceled when all waiters left, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("sharedCtx was not canceled after all waiters canceled")
	}

	// 3. 并发广播可变 map/slice/struct/pointer 引用时深拷贝隔离，并发修改零竞态零串改
	type mutablePayload struct {
		Tags      []string
		Meta      map[string]interface{}
		Matrix    [2][]int
		Timestamp time.Time
		CountPtr  *int
	}
	const callers = 16
	startLoad := make(chan struct{})
	releaseLoad := make(chan struct{})
	var loadOnce sync.Once
	var wgMut sync.WaitGroup
	results := make([]*mutablePayload, callers)
	errs := make([]error, callers)

	for i := 0; i < callers; i++ {
		wgMut.Add(1)
		go func(idx int) {
			defer wgMut.Done()
			val, err := store.RememberContext(context.Background(), "mutable_flight", time.Minute, func() (interface{}, error) {
				loadOnce.Do(func() { close(startLoad) })
				<-releaseLoad
				cnt := 42
				return &mutablePayload{
					Tags:      []string{"go", "cache"},
					Meta:      map[string]interface{}{"nested": map[string]int{"v": 1}, "nilSlice": ([]string)(nil), "nilMap": (map[string]int)(nil)},
					Matrix:    [2][]int{{1, 2}, {3, 4}},
					Timestamp: time.Unix(1_760_000_000, 123).UTC(),
					CountPtr:  &cnt,
				}, nil
			})
			if err != nil {
				errs[idx] = err
				return
			}
			p := val.(*mutablePayload)
			// 并发原地修改返回的 map / slice / array / pointer，若共享底层引用将在 -race 或运行时直接 panic
			p.Tags[0] = fmt.Sprintf("mutated-%d", idx)
			p.Meta[fmt.Sprintf("k-%d", idx)] = idx
			p.Meta["nested"].(map[string]int)[fmt.Sprintf("n-%d", idx)] = idx
			p.Matrix[0][0] = idx
			*p.CountPtr = idx
			results[idx] = p
		}(i)
	}
	<-startLoad
	time.Sleep(5 * time.Millisecond)
	close(releaseLoad)
	wgMut.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d failed: %v", i, err)
		}
		if results[i].Tags[0] != fmt.Sprintf("mutated-%d", i) || *results[i].CountPtr != i {
			t.Fatalf("caller %d payload corrupted by sibling goroutine: %+v", i, results[i])
		}
	}
	cachedRaw, found, err := store.Get("mutable_flight")
	if err != nil || !found {
		t.Fatalf("mutable_flight missing in cache: found=%t err=%v", found, err)
	}
	cachedPayload := cachedRaw.(*mutablePayload)
	if cachedPayload.Tags[0] != "go" || *cachedPayload.CountPtr != 42 {
		t.Fatalf("cached copy was mutated by callers: %+v", cachedPayload)
	}
}

// TestIssue603ConfigureStoreTagPrefixIsolatesTagNamespace 验证 StoreOptions.TagPrefix 真实隔离标签命名空间。
func TestIssue603ConfigureStoreTagPrefixIsolatesTagNamespace(t *testing.T) {
	backends := map[string]func(*testing.T) Driver{
		"memory": func(*testing.T) Driver { return cacheDriver.NewMemory() },
		"file": func(t *testing.T) Driver {
			d, err := cacheDriver.NewFile(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			return d
		},
		"redis": atomicOrderRedis,
	}
	for name, factory := range backends {
		t.Run(name, func(t *testing.T) {
			cfgCache := NewCache(nil, factory(t))

			// 默认 TagPrefix ("tag:") 下绑定 "item1" 到标签 "news"
			tDefault, err := cfgCache.Tag("news")
			if err != nil {
				t.Fatal(err)
			}
			if err := tDefault.Set("item1", "v1_default_ns", time.Hour); err != nil {
				t.Fatal(err)
			}

			// 切换 TagPrefix 为 "v2:"，在 "v2:" 标签空间绑定 "item2" 并执行 Tag("news").Flush()
			if err := cfgCache.ConfigureStore("default", StoreOptions{TagPrefix: "v2:"}); err != nil {
				t.Fatalf("ConfigureStore(TagPrefix: v2:) failed: %v", err)
			}
			tV2, err := cfgCache.Tag("news")
			if err != nil {
				t.Fatal(err)
			}
			if err := tV2.Set("item2", "v2_custom_ns", time.Hour); err != nil {
				t.Fatal(err)
			}
			if err := tV2.Flush(); err != nil {
				t.Fatalf("tV2.Flush() failed: %v", err)
			}

			// "item2" 已按 v2 标签清理，而旧默认命名空间下的 "item1" 不得被误清
			if _, found, err := cfgCache.Get("item2"); err != nil || found {
				t.Fatalf("item2 in v2 tag namespace survived Flush: found=%t err=%v", found, err)
			}
			if val, found, err := cfgCache.Get("item1"); err != nil || !found || val != "v1_default_ns" {
				t.Fatalf("TagPrefix was dead config! item1 in default namespace was wiped: val=%#v found=%t err=%v", val, found, err)
			}

			// 切回默认 TagPrefix 后清理默认 "news" 标签，"item1" 正常被删除
			if err := cfgCache.ConfigureStore("default", StoreOptions{TagPrefix: defaultTagPrefix}); err != nil {
				t.Fatal(err)
			}
			tBack, _ := cfgCache.Tag("news")
			if err := tBack.Flush(); err != nil {
				t.Fatal(err)
			}
			if _, found, err := cfgCache.Get("item1"); err != nil || found {
				t.Fatalf("item1 survived default namespace Flush: found=%t err=%v", found, err)
			}
		})
	}
}

// TestIssue603MemoryWithBudgetSupportsTimeValues 验证带字节预算的内存驱动放行 time.Time 并准确计入/释放预算。
func TestIssue603MemoryWithBudgetSupportsTimeValues(t *testing.T) {
	memBudget, err := cacheDriver.NewMemoryWithBudget(cacheDriver.MemoryBudget{
		MaxEntries:    64,
		MaxValueBytes: 16 << 10,
		MaxBytes:      64 << 10,
		MaxLocks:      16,
	})
	if err != nil {
		t.Fatal(err)
	}
	budgetCache := NewCache(nil, memBudget)

	now := time.Date(2026, 11, 18, 14, 30, 45, 123456789, time.FixedZone("CST", 8*3600))
	if err := budgetCache.Set("now", now, time.Minute); err != nil {
		t.Fatalf("NewMemoryWithBudget rejected time.Time: %v", err)
	}
	got, found, err := budgetCache.Get("now")
	if err != nil || !found {
		t.Fatalf("Get(now) failed: found=%t err=%v", found, err)
	}
	gotTime, ok := got.(time.Time)
	if !ok || !gotTime.Equal(now) {
		t.Fatalf("time.Time round-trip mismatch: got=%#v want=%#v", got, now)
	}

	type recordWithTime struct {
		ID        int64
		CreatedAt time.Time
		UpdatedAt *time.Time
		History   []time.Time
	}
	rec := recordWithTime{
		ID:        99,
		CreatedAt: now,
		UpdatedAt: &now,
		History:   []time.Time{now, now.Add(time.Second)},
	}
	if err := budgetCache.Set("record", rec, time.Minute); err != nil {
		t.Fatalf("NewMemoryWithBudget rejected struct containing time.Time: %v", err)
	}
	gotRec, found, err := budgetCache.Get("record")
	if err != nil || !found || !gotRec.(recordWithTime).CreatedAt.Equal(now) {
		t.Fatalf("struct with time.Time mismatch: got=%#v found=%t err=%v", gotRec, found, err)
	}
}

// TestIssue333RedisSequenceEvictionSelfHealsAllWriteAndInvalidationPaths 验证 Redis 淘汰代际序列键后全部读写与清理路径自动原子自愈。
func TestIssue333RedisSequenceEvictionSelfHealsAllWriteAndInvalidationPaths(t *testing.T) {
	for _, namespaced := range []bool{false, true} {
		mode := "direct"
		if namespaced {
			mode = "namespaced"
		}
		t.Run(mode, func(t *testing.T) {
			server := miniredis.RunT(t)
			const redisPrefix = "issue333:"
			rawRedis, err := redisDriver.NewRedis(map[string]interface{}{
				"host":   server.Host(),
				"port":   server.Server().Addr().Port,
				"prefix": redisPrefix,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = rawRedis.Close() })

			var drv Driver = rawRedis
			var ns *NamespaceDriver
			if namespaced {
				var nsErr error
				ns, nsErr = NewNamespaceDriver(rawRedis, "sub:", "")
				if nsErr != nil {
					t.Fatal(nsErr)
				}
				drv = ns
			}
			store := NewCache(nil, drv)
			physicalSeqKey := redisPrefix + store.fenceSequenceKey()
			if ns != nil {
				physicalSeqKey = redisPrefix + ns.key(store.fenceSequenceKey())
			}

			evictSequence := func() {
				t.Helper()
				if !server.Exists(physicalSeqKey) {
					t.Fatalf("expected physical sequence key %q to exist before simulated eviction", physicalSeqKey)
				}
				server.Del(physicalSeqKey)
			}

			// 推进代际水位到较高值（> 25），覆盖全局 Flush、前缀 ClearPrefix、精确键 Forget、标签 Flush 及已存高代际键
			for i := 0; i < 15; i++ {
				if err := store.Set("existing_high", fmt.Sprintf("v-%d", i), time.Hour); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.Flush(); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 10; i++ {
				if err := store.Set("existing_high", fmt.Sprintf("post-flush-%d", i), time.Hour); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.Set("pref:k1", "old", time.Hour); err != nil {
				t.Fatal(err)
			}
			if err := store.ClearPrefix("pref:"); err != nil {
				t.Fatal(err)
			}
			if err := store.Set("forget_me", "old", time.Hour); err != nil {
				t.Fatal(err)
			}
			if err := store.Forget("forget_me"); err != nil {
				t.Fatal(err)
			}
			if err := store.Set("counter_high", int64(100), time.Hour); err != nil {
				t.Fatal(err)
			}

			// 1. 淘汰序列后覆盖已有高代际键与写入曾失效键 / 全新键 (Set)
			evictSequence()
			if err := store.Set("existing_high", "healed_set", time.Hour); err != nil {
				t.Fatalf("Set(existing_high) failed with %v after sequence eviction", err)
			}
			if got, found, err := store.Get("existing_high"); err != nil || !found || got != "healed_set" {
				t.Fatalf("Get(existing_high) after self-heal mismatch: got=%#v found=%t err=%v", got, found, err)
			}
			evictSequence()
			if err := store.Set("pref:k1", "healed_pref", time.Hour); err != nil {
				t.Fatalf("Set(pref:k1) failed after sequence eviction: %v", err)
			}
			evictSequence()
			if err := store.Set("forget_me", "healed_forgotten", time.Hour); err != nil {
				t.Fatalf("Set(forget_me) failed after sequence eviction: %v", err)
			}
			evictSequence()
			if err := store.Set("brand_new_key", "healed_new", time.Hour); err != nil {
				t.Fatalf("Set(brand_new_key) failed after sequence eviction: %v", err)
			}

			// 2. 淘汰序列后执行 Update / Inc / Dec
			evictSequence()
			if err := store.Update("existing_high", time.Hour, func(cur interface{}, found bool) (interface{}, bool, error) {
				if !found || cur != "healed_set" {
					t.Fatalf("unexpected Update input: found=%t cur=%#v", found, cur)
				}
				return "healed_update", false, nil
			}); err != nil {
				t.Fatalf("Update failed after sequence eviction: %v", err)
			}
			evictSequence()
			if val, err := store.Inc("counter_high", 5); err != nil || val != 105 {
				t.Fatalf("Inc failed after sequence eviction: val=%d err=%v", val, err)
			}
			evictSequence()
			if val, err := store.Dec("counter_high", 3); err != nil || val != 102 {
				t.Fatalf("Dec failed after sequence eviction: val=%d err=%v", val, err)
			}

			// 3. 淘汰序列后执行 SetMany（同时包含高代际已有键与新键）
			evictSequence()
			if err := store.SetMany(map[string]interface{}{
				"existing_high": "healed_batch_existing",
				"batch_fresh":   "healed_batch_fresh",
			}, time.Hour); err != nil {
				t.Fatalf("SetMany failed after sequence eviction: %v", err)
			}
			if got, found, err := store.Get("existing_high"); err != nil || !found || got != "healed_batch_existing" {
				t.Fatalf("Get(existing_high) after SetMany mismatch: got=%#v found=%t err=%v", got, found, err)
			}

			// 4. 淘汰序列后执行 RememberWithLock
			if err := store.Forget("remember_target"); err != nil {
				t.Fatal(err)
			}
			evictSequence()
			remVal, err := store.RememberWithLock("remember_target", time.Hour, time.Second, func() (interface{}, error) {
				return "healed_remember", nil
			})
			if err != nil || remVal != "healed_remember" {
				t.Fatalf("RememberWithLock failed after sequence eviction: val=%#v err=%v", remVal, err)
			}

			// 5. 淘汰序列后执行 Forget / Tag.Flush / ClearPrefix / Flush
			tagged, err := store.Tag("t333")
			if err != nil {
				t.Fatal(err)
			}
			if err := tagged.Set("tagged_item", "tv", time.Hour); err != nil {
				t.Fatal(err)
			}
			evictSequence()
			if err := tagged.Flush(); err != nil {
				t.Fatalf("Tag.Flush failed after sequence eviction: %v", err)
			}
			if _, found, err := store.Get("tagged_item"); err != nil || found {
				t.Fatalf("tagged_item survived Tag.Flush after sequence eviction: found=%t err=%v", found, err)
			}

			evictSequence()
			if err := store.Forget("batch_fresh"); err != nil {
				t.Fatalf("Forget failed after sequence eviction: %v", err)
			}
			evictSequence()
			if err := store.ClearPrefix("pref:"); err != nil {
				t.Fatalf("ClearPrefix failed after sequence eviction: %v", err)
			}
			evictSequence()
			if err := store.Flush(); err != nil {
				t.Fatalf("Flush failed after sequence eviction: %v", err)
			}

			// 6. 淘汰序列后的高并发混合写入自愈压测
			if err := store.Set("shared_hot", "init", time.Hour); err != nil {
				t.Fatal(err)
			}
			if err := store.Set("shared_cnt", int64(0), time.Hour); err != nil {
				t.Fatal(err)
			}
			evictSequence()

			const workers = 12
			var wg sync.WaitGroup
			errCh := make(chan error, workers*2)
			for i := 0; i < workers; i++ {
				wg.Add(1)
				go func(id int) {
					defer wg.Done()
					errCh <- store.Set(fmt.Sprintf("concurrent_k_%d", id), id, time.Hour)
					_, incErr := store.Inc("shared_cnt", 1)
					errCh <- incErr
				}(i)
			}
			wg.Wait()
			close(errCh)
			for opErr := range errCh {
				if opErr != nil {
					t.Fatalf("concurrent write/inc after sequence eviction failed: %v", opErr)
				}
			}
			if cnt, found, err := store.Get("shared_cnt"); err != nil || !found || cnt != int64(workers) {
				t.Fatalf("concurrent counter after sequence eviction mismatch: got=%#v found=%t err=%v", cnt, found, err)
			}
		})
	}
}

type pausedSequenceAndUpdateDriver struct {
	*cacheDriver.Memory
	pauseOnSeq bool
	targetKey  string
	pauseOnUpd bool
	started    chan struct{}
	release    chan struct{}
	once       sync.Once
}

func (d *pausedSequenceAndUpdateDriver) Inc(key string, step int64) (int64, error) {
	val, err := d.Memory.Inc(key, step)
	if d.pauseOnSeq && strings.HasSuffix(key, ":sequence") {
		d.once.Do(func() { close(d.started) })
		<-d.release
	}
	return val, err
}

func (d *pausedSequenceAndUpdateDriver) Update(key string, ttl time.Duration, update func(interface{}, bool) (interface{}, bool, error)) error {
	if d.pauseOnUpd && key == d.targetKey {
		d.once.Do(func() { close(d.started) })
		<-d.release
	}
	return d.Memory.Update(key, ttl, update)
}

// TestIssue602WatermarkRetirementBlocksInFlightOlderWriters 验证已完成水位被同桶后续失效裁剪后，仍在途的旧代写者绝不能复活已删除数据，且同桶存活键与新写者不受影响。
func TestIssue602WatermarkRetirementBlocksInFlightOlderWriters(t *testing.T) {
	for _, pauseStage := range []string{"after_generation_alloc", "before_atomic_commit"} {
		t.Run(pauseStage, func(t *testing.T) {
			raw := cacheDriver.NewMemory()
			fast := NewCache(nil, raw)

			const victim = "untagged_victim"
			targetBucket := keyInvalidationBucket(victim)

			survivor := ""
			churnKeys := make([]string, 0, maxCacheInvalidationScopes+64)
			for i := 0; len(churnKeys) < maxCacheInvalidationScopes+64 || survivor == ""; i++ {
				candidate := fmt.Sprintf("bucket_peer_%d", i)
				if keyInvalidationBucket(candidate) != targetBucket {
					continue
				}
				if survivor == "" {
					survivor = candidate
					continue
				}
				churnKeys = append(churnKeys, candidate)
			}

			if err := fast.Set(survivor, int64(40), time.Hour); err != nil {
				t.Fatal(err)
			}
			if err := fast.Set(victim, "initial", time.Hour); err != nil {
				t.Fatal(err)
			}

			slowBackend := &pausedSequenceAndUpdateDriver{
				Memory:     raw,
				pauseOnSeq: pauseStage == "after_generation_alloc",
				pauseOnUpd: pauseStage == "before_atomic_commit",
				targetKey:  victim,
				started:    make(chan struct{}),
				release:    make(chan struct{}),
			}
			slow := NewCache(nil, slowBackend)

			errCh := make(chan error, 1)
			go func() {
				errCh <- slow.Set(victim, "resurrected_stale", time.Hour)
			}()
			<-slowBackend.started

			// Forget 删除 victim 并标记水位完成，随后同桶大量失效将该完成态水位裁剪出桶并推进 retired 水位。
			if err := fast.Forget(victim); err != nil {
				t.Fatalf("Forget(victim) failed: %v", err)
			}
			for i, k := range churnKeys {
				if err := fast.Forget(k); err != nil {
					t.Fatalf("churn Forget #%d (%s) failed: %v", i, k, err)
				}
			}

			close(slowBackend.release)
			writeErr := <-errCh
			if !errors.Is(writeErr, ErrCacheLockLost) {
				t.Fatalf("expected paused older Set to fail with ErrCacheLockLost after watermark retirement, got %v", writeErr)
			}
			if val, found, err := fast.Get(victim); err != nil || found {
				t.Fatalf("older Set resurrected deleted key after watermark eviction! val=%#v found=%t err=%v", val, found, err)
			}

			// 同桶早期已提交且从未失效的存活键保持可读且可正常原子递增。
			if val, found, err := fast.Get(survivor); err != nil || !found || val != int64(40) {
				t.Fatalf("unrelated live key in same bucket lost visibility: val=%#v found=%t err=%v", val, found, err)
			}
			if next, err := fast.Inc(survivor, 2); err != nil || next != 42 {
				t.Fatalf("Inc on live key in retired bucket failed: next=%d err=%v", next, err)
			}

			// 裁剪之后新发起的写入正常分配高于 retired 的代际并成功提交。
			if err := fast.Set(victim, "fresh_after_forget", time.Hour); err != nil {
				t.Fatalf("fresh Set after retirement failed: %v", err)
			}
			if val, found, err := fast.Get(victim); err != nil || !found || val != "fresh_after_forget" {
				t.Fatalf("fresh Set value mismatch: val=%#v found=%t err=%v", val, found, err)
			}
		})
	}
}
