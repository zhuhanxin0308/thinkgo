package database

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

	"github.com/zhuhanxin0308/thinkgo/v3/db"
	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
	"github.com/zhuhanxin0308/thinkgo/v3/internal/cachekey"
)

// TestIssue601PreciseIntegersAndWideNumbers 验证 int64、uint64 和宽整数 json.Number 在 DB 驱动无损往返。
func TestIssue601PreciseIntegersAndWideNumbers(t *testing.T) {
	conn := newRecordingCacheConn()
	backend, err := NewDB(conn, "think_cache")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		value interface{}
	}{
		{"max_int64", int64(math.MaxInt64)},
		{"min_int64", int64(math.MinInt64)},
		{"zero_int64", int64(0)},
		{"max_uint64", uint64(math.MaxUint64)},
		{"zero_uint64", uint64(0)},
		{"json_number_int64", json.Number("9223372036854775807")},
		{"json_number_uint64", json.Number("18446744073709551615")},
		{"json_number_neg_wide", json.Number("-9223372036854775809")},
		{"json_number_wide", json.Number("123456789012345678901234567890")},
	}
	if strconv.IntSize == 64 {
		wide := int64(1) << 53
		bigInt := int(wide + 7)
		bigUint := uint(wide + 9)
		cases = append(cases,
			struct {
				name  string
				value interface{}
			}{"big_int", bigInt},
			struct {
				name  string
				value interface{}
			}{"big_neg_int", -bigInt},
			struct {
				name  string
				value interface{}
			}{"big_uint", bigUint},
		)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := backend.Set(tc.name, tc.value, time.Minute); err != nil {
				t.Fatalf("Set(%s) failed: %v", tc.name, err)
			}
			got, found, err := backend.Get(tc.name)
			if err != nil || !found || got != tc.value {
				t.Fatalf("Get(%s) mismatch: got=%#v (%T) found=%t err=%v, want=%#v (%T)", tc.name, got, got, found, err, tc.value, tc.value)
			}
		})
	}

	if err := backend.Set("decimal_num", json.Number("1.5"), 0); err != nil {
		t.Fatal(err)
	}
	if got, found, err := backend.Get("decimal_num"); err != nil || !found || got != 1.5 {
		t.Fatalf("decimal json.Number mismatch: got=%#v found=%t err=%v", got, found, err)
	}

	for _, bad := range []json.Number{"", "bad", "01", "+1", "1e"} {
		if err := backend.Set("bad_num", bad, 0); !errors.Is(err, ErrInvalidCacheValue) {
			t.Fatalf("expected ErrInvalidCacheValue for %q, got %v", bad, err)
		}
	}

	// 验证损坏的精确整数信封失败关闭为 ErrCorruptCacheEntry。
	for idx, rawPayload := range []string{
		`{"__thinkgo_db_value":"thinkgo-db-value-v1","value":"123"}`,
		`{"__thinkgo_db_value":"thinkgo-db-value-v1","value":"123","encoding":"integer:int64","extra":true}`,
		`{"__thinkgo_db_value":"thinkgo-db-value-v1","value":"not-a-number","encoding":"integer:int64"}`,
	} {
		key := fmt.Sprintf("corrupt_env_%d", idx)
		physical := mustStorageKey(t, backend, key)
		conn.rows[physical] = map[string]interface{}{"key": physical, "value": rawPayload, "expiry": int64(0)}
		if _, _, err := backend.Get(key); !errors.Is(err, ErrCorruptCacheEntry) {
			t.Fatalf("corrupt envelope %d did not fail with ErrCorruptCacheEntry: %v", idx, err)
		}
	}

	// 验证 Inc/Dec 能读取精确整数信封并在超界 uint64 上报错。
	if err := backend.Set("too_big_counter", uint64(math.MaxInt64)+1, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Inc("too_big_counter", 1); !errors.Is(err, ErrInvalidCounterValue) {
		t.Fatalf("expected ErrInvalidCounterValue, got %v", err)
	}
	if err := backend.Set("zero_step", int64(42), 0); err != nil {
		t.Fatal(err)
	}
	if val, err := backend.Inc("zero_step", 0); err != nil || val != 42 {
		t.Fatalf("zero step counter read failed: %d %v", val, err)
	}
}

// TestIssue601SubMillisecondTTLPreventsDualLockHolders 验证亚毫秒正 TTL 向上取整，杜绝双持有者同时获锁。
func TestIssue601SubMillisecondTTLPreventsDualLockHolders(t *testing.T) {
	// 验证毫秒边界附近（如 .999995ms + 100µs）也不会在 100µs 到期前提前跨毫秒失效。
	baseSec := int64(1_760_000_000)
	nearBoundary := time.Unix(baseSec, 999_995_000)
	deadlineMillis := dbExpiryMillis(nearBoundary, 100*time.Microsecond)
	after6us := nearBoundary.Add(6 * time.Microsecond).UnixMilli()
	if deadlineMillis <= after6us {
		t.Fatalf("sub-ms lock crossed boundary prematurely: expiry=%d after6us=%d", deadlineMillis, after6us)
	}
	exactBoundary := time.Unix(baseSec, 0)
	if got := dbExpiryMillis(exactBoundary, time.Nanosecond); got != exactBoundary.UnixMilli()+1 {
		t.Fatalf("1ns TTL must ceil to +1ms: got=%d want=%d", got, exactBoundary.UnixMilli()+1)
	}
	if got := dbExpiryMillis(exactBoundary, 0); got != 0 {
		t.Fatalf("zero TTL must remain permanent (0): got=%d", got)
	}

	conn := newRecordingCacheConn()
	backend, err := NewDB(conn, "think_cache")
	if err != nil {
		t.Fatal(err)
	}
	ok1, err := backend.AcquireLock("job", "owner-1", 500*time.Microsecond)
	if err != nil || !ok1 {
		t.Fatalf("first sub-ms lock acquire failed: ok=%t err=%v", ok1, err)
	}
	conn.lock.Lock()
	lockRow := conn.rows[dbLockStorageKey("job")]
	lockExpiry, _ := parseDBExpiry(lockRow["expiry"])
	conn.lock.Unlock()
	if lockExpiry <= 0 {
		t.Fatalf("invalid lock expiry: %d", lockExpiry)
	}
	// 在租约尚未到期时，竞争者绝不能同时抢占获锁。
	if time.Now().UnixMilli() < lockExpiry {
		ok2, err := backend.AcquireLock("job", "owner-2", time.Second)
		if err != nil || ok2 {
			t.Fatalf("dual lock holders acquired simultaneously: ok2=%t err=%v", ok2, err)
		}
	}
	time.Sleep(5 * time.Millisecond)
	ok3, err := backend.AcquireLock("job", "owner-2", time.Second)
	if err != nil || !ok3 {
		t.Fatalf("expired sub-ms lock could not be re-acquired: ok3=%t err=%v", ok3, err)
	}
	if renewed, err := backend.RenewLock("job", "owner-2", 500*time.Microsecond); err != nil || !renewed {
		t.Fatalf("sub-ms RenewLock failed: renewed=%t err=%v", renewed, err)
	}
}

type upsertInterleavingConn struct {
	*recordingCacheConn
	targetKey string
	once      sync.Once
}

func (c *upsertInterleavingConn) Update(ctx context.Context, request db.UpdateRequest) (db.UpdateResult, error) {
	result, err := c.recordingCacheConn.Update(ctx, request)
	key, _ := request.Data()["key"].(string)
	if key == c.targetKey && result.Affected == 0 {
		c.once.Do(func() {
			c.lock.Lock()
			c.rows[key] = map[string]interface{}{
				"key":    key,
				"value":  `"short_lived_other"`,
				"expiry": time.Now().Add(20 * time.Millisecond).UnixMilli(),
			}
			c.lock.Unlock()
		})
	}
	return result, err
}

// TestIssue601UpsertDoesNotSwallowConcurrentWrite 验证 Update 返回 0 后若有并发插入不同记录，不会假阳性丢弃本次写入。
func TestIssue601UpsertDoesNotSwallowConcurrentWrite(t *testing.T) {
	base := newRecordingCacheConn()
	interleaved := &upsertInterleavingConn{recordingCacheConn: base}
	backend, err := NewDB(interleaved, "think_cache")
	if err != nil {
		t.Fatal(err)
	}
	interleaved.targetKey = mustStorageKey(t, backend, "upsert-race")
	if err := backend.Set("upsert-race", "permanent_winner", 0); err != nil {
		t.Fatalf("Set failed under concurrent insert race: %v", err)
	}
	got, found, err := backend.Get("upsert-race")
	if err != nil || !found || got != "permanent_winner" {
		t.Fatalf("upsert swallowed write! got=%#v found=%t err=%v", got, found, err)
	}
	base.lock.Lock()
	storedExpiry := base.rows[interleaved.targetKey]["expiry"]
	base.lock.Unlock()
	if storedExpiry != int64(0) {
		t.Fatalf("upsert lost permanent TTL: expiry=%#v", storedExpiry)
	}
}

// TestIssue601Supports1024ByteLogicalKeys 验证 256..1024 字节逻辑键（含命名空间包装）在 DB 驱动正常工作。
func TestIssue601Supports1024ByteLogicalKeys(t *testing.T) {
	conn := newRecordingCacheConn()
	backend, err := NewDB(conn, "think_cache")
	if err != nil {
		t.Fatal(err)
	}
	for _, length := range []int{256, 300, 1024} {
		key := strings.Repeat("k", length)
		if err := backend.Set(key, int64(length), time.Minute); err != nil {
			t.Fatalf("Set(%d-byte key) failed: %v", length, err)
		}
		got, found, err := backend.Get(key)
		if err != nil || !found || got != int64(length) {
			t.Fatalf("Get(%d-byte key) failed: got=%#v found=%t err=%v", length, got, found, err)
		}
		framedKey := cachekey.Namespace("tenant-prefix:") + key
		if err := backend.Set(framedKey, "framed-ok", time.Minute); err != nil {
			t.Fatalf("Set(framed %d-byte key) failed: %v", length, err)
		}
		if got, found, err := backend.Get(framedKey); err != nil || !found || got != "framed-ok" {
			t.Fatalf("Get(framed %d-byte key) failed: got=%#v found=%t err=%v", length, got, found, err)
		}
	}
	if err := backend.Set(strings.Repeat("x", maxDBCacheKeyBytes+1), "too-long", 0); !errors.Is(err, ErrCacheKeyTooLong) {
		t.Fatalf("expected ErrCacheKeyTooLong for 1025-byte key, got %v", err)
	}
	if err := backend.validatePhysicalKey(strings.Repeat("p", maxDBPhysicalKeyBytes+1)); !errors.Is(err, ErrCacheKeyTooLong) {
		t.Fatalf("expected ErrCacheKeyTooLong for >255 physical key, got %v", err)
	}
}

// TestDBAtomicUpdateAndExpiryOperations 验证无 CGO 下 DB 原子读改写、TTL 保留、条件过期与错误边界。
func TestDBAtomicUpdateAndExpiryOperations(t *testing.T) {
	conn := newRecordingCacheConn()
	backend, err := NewDB(conn, "think_cache")
	if err != nil {
		t.Fatal(err)
	}
	if !backend.SupportsAtomicFencing() {
		// DB 显式走共享代号清理而非前缀扫描 fencing。
	}
	// 创建与更新
	if err := backend.Update("atomic-k", time.Minute, func(val interface{}, found bool) (interface{}, bool, error) {
		if found || val != nil {
			t.Fatalf("expected miss on initial Update: found=%t val=%#v", found, val)
		}
		return int64(math.MaxInt64 - 1), false, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := backend.UpdatePreserveTTL("atomic-k", func(val interface{}, found bool) (interface{}, bool, error) {
		if !found || val != int64(math.MaxInt64-1) {
			t.Fatalf("unexpected current value: found=%t val=%#v", found, val)
		}
		return int64(math.MaxInt64), false, nil
	}); err != nil {
		t.Fatal(err)
	}
	got, found, err := backend.Get("atomic-k")
	if err != nil || !found || got != int64(math.MaxInt64) {
		t.Fatalf("UpdatePreserveTTL lost precision or value: got=%#v found=%t err=%v", got, found, err)
	}

	// 条件重置为永久项
	if err := backend.UpdatePreserveTTLConditionally("atomic-k", func(val interface{}, found bool) (interface{}, bool, bool, error) {
		return val, false, false, nil
	}); err != nil {
		t.Fatal(err)
	}
	physical := mustStorageKey(t, backend, "atomic-k")
	if conn.rows[physical]["expiry"] != int64(0) {
		t.Fatalf("expected permanent expiry 0, got %#v", conn.rows[physical]["expiry"])
	}

	// reportUnchanged 下的幂等 UpdateWithExpiryContext
	conn.lock.Lock()
	conn.reportUnchanged = true
	conn.lock.Unlock()
	if err := backend.Update("atomic-k", 0, func(val interface{}, _ bool) (interface{}, bool, error) {
		return val, false, nil
	}); err != nil {
		t.Fatalf("unchanged atomic update failed: %v", err)
	}
	conn.lock.Lock()
	conn.reportUnchanged = false
	conn.lock.Unlock()

	// 过去时间的 expiryOverride 立即删除
	past := time.Now().Add(-time.Second)
	if err := backend.UpdateWithExpiryContext(context.Background(), "atomic-k", time.Minute, func(interface{}, bool, time.Time) (interface{}, bool, *time.Time, error) {
		return "stale", false, &past, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := backend.Get("atomic-k"); err != nil || found {
		t.Fatalf("past expiryOverride should remove item: found=%t err=%v", found, err)
	}

	// 缺失键上 remove=true
	if err := backend.Update("atomic-k", 0, func(interface{}, bool) (interface{}, bool, error) {
		return nil, true, nil
	}); err != nil {
		t.Fatal(err)
	}

	// 错误与边界检查
	var nilDB *DB
	if err := nilDB.Update("k", 0, func(interface{}, bool) (interface{}, bool, error) { return 1, false, nil }); !errors.Is(err, ErrInvalidCacheDatabase) {
		t.Fatalf("expected ErrInvalidCacheDatabase, got %v", err)
	}
	if err := backend.Update("k", 0, nil); !errors.Is(err, ErrNilAtomicUpdate) {
		t.Fatalf("expected ErrNilAtomicUpdate, got %v", err)
	}
	if err := backend.UpdatePreserveTTL("k", nil); !errors.Is(err, ErrNilAtomicUpdate) {
		t.Fatalf("expected ErrNilAtomicUpdate, got %v", err)
	}
	if err := backend.UpdatePreserveTTLConditionally("k", nil); !errors.Is(err, ErrNilAtomicUpdate) {
		t.Fatalf("expected ErrNilAtomicUpdate, got %v", err)
	}
	if err := backend.Update("k", -time.Second, func(interface{}, bool) (interface{}, bool, error) { return 1, false, nil }); !errors.Is(err, ErrInvalidDriverTTL) {
		t.Fatalf("expected ErrInvalidDriverTTL, got %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := backend.UpdateContext(canceled, "k", 0, func(interface{}, bool) (interface{}, bool, error) { return 1, false, nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if nilDB.CacheResourceIdentity() != "" {
		t.Fatal("nil DB should return empty CacheResourceIdentity")
	}
	if _, err := backend.AcquireLock("k", "", time.Second); !errors.Is(err, ErrInvalidCacheLock) {
		t.Fatalf("expected ErrInvalidCacheLock, got %v", err)
	}
	if _, err := backend.AcquireLock("k", "owner", 0); !errors.Is(err, ErrInvalidCacheLock) {
		t.Fatalf("expected ErrInvalidCacheLock, got %v", err)
	}
	if _, err := backend.ReleaseLock("k", ""); !errors.Is(err, ErrInvalidCacheLock) {
		t.Fatalf("expected ErrInvalidCacheLock, got %v", err)
	}
	if _, err := backend.RenewLock("k", "", time.Second); !errors.Is(err, ErrInvalidCacheLock) {
		t.Fatalf("expected ErrInvalidCacheLock, got %v", err)
	}
	if err := backend.Set("neg-ttl", "v", -time.Millisecond); !errors.Is(err, ErrInvalidDriverTTL) {
		t.Fatalf("expected ErrInvalidDriverTTL, got %v", err)
	}

	// 并发乐观 CAS 原子更新验证
	const workers = 16
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- backend.Update("cas-counter", time.Minute, func(val interface{}, found bool) (interface{}, bool, error) {
				if !found {
					return int64(1), false, nil
				}
				count, ok := val.(int64)
				if !ok {
					return nil, false, fmt.Errorf("并发 CAS 需要 int64，实际为 %T", val)
				}
				return count + 1, false, nil
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Update failed: %v", err)
		}
	}
	if got, found, err := backend.Get("cas-counter"); err != nil || !found || got != int64(workers) {
		t.Fatalf("concurrent Update final value mismatch: got=%#v found=%t err=%v", got, found, err)
	}

	// parseDBExpiry 多类型兼容验证
	if exp, err := parseDBExpiry(nil); err != nil || exp != 0 {
		t.Fatalf("nil expiry failed: %d %v", exp, err)
	}
	if exp, err := parseDBExpiry([]byte("12345")); err != nil || exp != 12345 {
		t.Fatalf("[]byte expiry failed: %d %v", exp, err)
	}
}

// pureGoLeaseDriver 为无 CGO 环境提供内存级 SQL 事务模拟，完整覆盖 lease.go 的事务提交与回滚分支。
type pureGoLeaseDriver struct {
	mu        sync.Mutex
	committed map[string]pureGoLeaseRow
	staged    map[string]*pureGoLeaseRow
	inTx      bool
}

type pureGoLeaseRow struct {
	key    string
	value  string
	expiry int64
}

type pureGoLeaseTx struct{ d *pureGoLeaseDriver }

func (tx *pureGoLeaseTx) Commit() error {
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

func (tx *pureGoLeaseTx) Rollback() error {
	tx.d.mu.Lock()
	defer tx.d.mu.Unlock()
	tx.d.staged = nil
	tx.d.inTx = false
	return nil
}

func (d *pureGoLeaseDriver) getRowLocked(key string) (pureGoLeaseRow, bool) {
	if d.inTx && d.staged != nil {
		if staged, exists := d.staged[key]; exists {
			if staged == nil {
				return pureGoLeaseRow{}, false
			}
			return *staged, true
		}
	}
	row, ok := d.committed[key]
	return row, ok
}

func (d *pureGoLeaseDriver) setRowLocked(row pureGoLeaseRow) {
	if d.inTx {
		copyRow := row
		d.staged[row.key] = &copyRow
		return
	}
	d.committed[row.key] = row
}

func (d *pureGoLeaseDriver) delRowLocked(key string) {
	if d.inTx {
		d.staged[key] = nil
		return
	}
	delete(d.committed, key)
}

type pureGoStmt struct {
	d     *pureGoLeaseDriver
	query string
}

func (s *pureGoStmt) Close() error  { return nil }
func (s *pureGoStmt) NumInput() int { return -1 }
func (s *pureGoStmt) Exec(args []driver.Value) (driver.Result, error) {
	named := make([]driver.NamedValue, len(args))
	for i, v := range args {
		named[i] = driver.NamedValue{Ordinal: i + 1, Value: v}
	}
	return s.d.ExecContext(context.Background(), s.query, named)
}
func (s *pureGoStmt) Query(args []driver.Value) (driver.Rows, error) {
	named := make([]driver.NamedValue, len(args))
	for i, v := range args {
		named[i] = driver.NamedValue{Ordinal: i + 1, Value: v}
	}
	return s.d.QueryContext(context.Background(), s.query, named)
}
func (s *pureGoStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	return s.d.ExecContext(ctx, s.query, args)
}
func (s *pureGoStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	return s.d.QueryContext(ctx, s.query, args)
}

func (d *pureGoLeaseDriver) Connect(context.Context) (driver.Conn, error) { return d, nil }
func (d *pureGoLeaseDriver) Driver() driver.Driver                        { return d }
func (d *pureGoLeaseDriver) Open(string) (driver.Conn, error)             { return d, nil }
func (d *pureGoLeaseDriver) Close() error                                 { return nil }
func (d *pureGoLeaseDriver) Prepare(query string) (driver.Stmt, error) {
	return &pureGoStmt{d: d, query: query}, nil
}
func (d *pureGoLeaseDriver) Begin() (driver.Tx, error) {
	return d.BeginTx(context.Background(), driver.TxOptions{})
}
func (d *pureGoLeaseDriver) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.inTx = true
	d.staged = make(map[string]*pureGoLeaseRow)
	return &pureGoLeaseTx{d: d}, nil
}

func (d *pureGoLeaseDriver) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(args) == 0 {
		return &pureGoRows{}, nil
	}
	key := fmt.Sprint(args[0].Value)
	row, ok := d.getRowLocked(key)
	if !ok {
		return &pureGoRows{}, nil
	}
	_ = query
	return &pureGoRows{
		cols: []string{"key", "value", "expiry"},
		rows: [][]driver.Value{{row.key, row.value, row.expiry}},
	}, nil
}

func (d *pureGoLeaseDriver) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	upper := strings.ToUpper(strings.TrimSpace(query))
	switch {
	case strings.HasPrefix(upper, "INSERT"):
		// Columns sorted alphabetically by builder: expiry, key, value
		var row pureGoLeaseRow
		if strings.Index(upper, "EXPIRY") < strings.Index(upper, "KEY") {
			exp, _ := strconv.ParseInt(fmt.Sprint(args[0].Value), 10, 64)
			row = pureGoLeaseRow{expiry: exp, key: fmt.Sprint(args[1].Value), value: fmt.Sprint(args[2].Value)}
		} else {
			exp, _ := strconv.ParseInt(fmt.Sprint(args[2].Value), 10, 64)
			row = pureGoLeaseRow{key: fmt.Sprint(args[0].Value), value: fmt.Sprint(args[1].Value), expiry: exp}
		}
		if _, exists := d.getRowLocked(row.key); exists {
			return nil, errors.New("duplicate key")
		}
		d.setRowLocked(row)
		return driver.RowsAffected(1), nil
	case strings.HasPrefix(upper, "DELETE"):
		key := fmt.Sprint(args[0].Value)
		row, exists := d.getRowLocked(key)
		if !exists {
			return driver.RowsAffected(0), nil
		}
		if len(args) == 3 {
			owner := fmt.Sprint(args[1].Value)
			now, _ := strconv.ParseInt(fmt.Sprint(args[2].Value), 10, 64)
			if row.value != owner || row.expiry <= now {
				return driver.RowsAffected(0), nil
			}
		}
		d.delRowLocked(key)
		return driver.RowsAffected(1), nil
	case strings.HasPrefix(upper, "UPDATE"):
		// Lease guard update: UPDATE table SET value = ? WHERE key = ? AND value = ? AND expiry > ?
		if len(args) == 4 && strings.Contains(upper, "EXPIRY") && strings.Contains(upper, ">") {
			newVal := fmt.Sprint(args[0].Value)
			key := fmt.Sprint(args[1].Value)
			wantVal := fmt.Sprint(args[2].Value)
			now, _ := strconv.ParseInt(fmt.Sprint(args[3].Value), 10, 64)
			row, exists := d.getRowLocked(key)
			if !exists || row.value != wantVal || row.expiry <= now {
				return driver.RowsAffected(0), nil
			}
			if strings.Contains(upper, "SET \"EXPIRY\"") || strings.Contains(upper, "SET `EXPIRY`") {
				exp, _ := strconv.ParseInt(newVal, 10, 64)
				row.expiry = exp
			} else {
				row.value = newVal
			}
			d.setRowLocked(row)
			return driver.RowsAffected(1), nil
		}
		// Lease restore update: UPDATE table SET value = ? WHERE key = ?
		if len(args) == 2 {
			newVal := fmt.Sprint(args[0].Value)
			key := fmt.Sprint(args[1].Value)
			row, exists := d.getRowLocked(key)
			if !exists {
				return driver.RowsAffected(0), nil
			}
			row.value = newVal
			d.setRowLocked(row)
			return driver.RowsAffected(1), nil
		}
		// Record overwrite update inside lease: SET expiry = ?, key = ?, value = ? WHERE key = ?
		if len(args) == 4 {
			exp, _ := strconv.ParseInt(fmt.Sprint(args[0].Value), 10, 64)
			key := fmt.Sprint(args[3].Value)
			val := fmt.Sprint(args[2].Value)
			row, exists := d.getRowLocked(key)
			if !exists {
				return driver.RowsAffected(0), nil
			}
			row.expiry = exp
			row.value = val
			d.setRowLocked(row)
			return driver.RowsAffected(1), nil
		}
	}
	return driver.RowsAffected(0), nil
}

type pureGoRows struct {
	cols []string
	rows [][]driver.Value
	idx  int
}

func (r *pureGoRows) Columns() []string { return r.cols }
func (r *pureGoRows) Close() error      { return nil }
func (r *pureGoRows) Next(dest []driver.Value) error {
	if r.idx >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.idx])
	r.idx++
	return nil
}

// TestDBLeaseGuardedUpdatePureGo 在 CGO_ENABLED=0 下完整覆盖 UpdateIfLockOwnerContext 的成功、回滚、删除与租约丢失分支。
func TestDBLeaseGuardedUpdatePureGo(t *testing.T) {
	mock := &pureGoLeaseDriver{committed: make(map[string]pureGoLeaseRow)}
	pool := sql.OpenDB(mock)
	pool.SetMaxOpenConns(1)
	conn := db.NewSQLConnection(pool, &builder.Sqlite{})
	t.Cleanup(func() { _ = conn.Close() })
	backend, err := NewDB(conn, "cache_records")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := backend.AcquireLock("lease", "owner-a", time.Minute); err != nil || !ok {
		t.Fatalf("AcquireLock failed: ok=%t err=%v", ok, err)
	}
	// 首次提交高精度 int64
	ok, err := backend.UpdateIfLockOwnerContext(context.Background(), "item", 500*time.Microsecond, "lease", "owner-a", func(cur interface{}, found bool) (interface{}, bool, error) {
		if found || cur != nil {
			t.Fatalf("unexpected hit: found=%t cur=%#v", found, cur)
		}
		return int64(math.MaxInt64), false, nil
	})
	if err != nil || !ok {
		t.Fatalf("initial leased commit failed: ok=%t err=%v", ok, err)
	}
	if got, found, err := backend.Get("item"); err != nil || !found || got != int64(math.MaxInt64) {
		t.Fatalf("leased int64 precision lost: got=%#v found=%t err=%v", got, found, err)
	}
	// 回调错误触发事务回滚并保持 owner 完好
	wantErr := errors.New("abort leased update")
	if ok, err := backend.UpdateIfLockOwnerContext(context.Background(), "item", time.Minute, "lease", "owner-a", func(interface{}, bool) (interface{}, bool, error) {
		return "bad", false, wantErr
	}); ok || !errors.Is(err, wantErr) {
		t.Fatalf("expected rollback error, got ok=%t err=%v", ok, err)
	}
	// 覆盖更新与删除
	if ok, err := backend.UpdateIfLockOwnerContext(context.Background(), "item", time.Minute, "lease", "owner-a", func(interface{}, bool) (interface{}, bool, error) {
		return "updated", false, nil
	}); err != nil || !ok {
		t.Fatalf("overwrite leased record failed: ok=%t err=%v", ok, err)
	}
	if ok, err := backend.UpdateIfLockOwnerContext(context.Background(), "item", 0, "lease", "owner-a", func(interface{}, bool) (interface{}, bool, error) {
		return nil, true, nil
	}); err != nil || !ok {
		t.Fatalf("delete leased record failed: ok=%t err=%v", ok, err)
	}
	if _, found, err := backend.Get("item"); err != nil || found {
		t.Fatalf("leased delete did not remove item: found=%t err=%v", found, err)
	}
	// 错误 owner 返回 committed=false, err=nil
	if ok, err := backend.UpdateIfLockOwnerContext(context.Background(), "item", time.Minute, "lease", "wrong-owner", func(interface{}, bool) (interface{}, bool, error) {
		return "never", false, nil
	}); err != nil || ok {
		t.Fatalf("wrong owner should return false, nil; got ok=%t err=%v", ok, err)
	}
	// 参数校验边界
	if _, err := backend.UpdateIfLockOwnerContext(context.Background(), "item", 0, "", "owner-a", func(interface{}, bool) (interface{}, bool, error) { return 1, false, nil }); !errors.Is(err, ErrInvalidCacheLock) {
		t.Fatalf("expected ErrInvalidCacheLock, got %v", err)
	}
	if _, err := backend.UpdateIfLockOwnerContext(context.Background(), "item", 0, "lease", "owner-a", nil); !errors.Is(err, ErrNilAtomicUpdate) {
		t.Fatalf("expected ErrNilAtomicUpdate, got %v", err)
	}
	if _, err := backend.UpdateIfLockOwnerContext(context.Background(), "item", -time.Second, "lease", "owner-a", func(interface{}, bool) (interface{}, bool, error) { return 1, false, nil }); !errors.Is(err, ErrInvalidDriverTTL) {
		t.Fatalf("expected ErrInvalidDriverTTL, got %v", err)
	}
}
