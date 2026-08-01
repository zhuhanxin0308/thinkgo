package driver

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"sync"
	"time"

	"thinkgo/framework/db"
)

const (
	maxDBCacheKeyBytes       = 255
	millisecondEpochBoundary = int64(1_000_000_000_000)
	dbUpsertAttempts         = 2
	dbLockAcquireAttempts    = 3
	dbLockStoragePrefix      = "__thinkgo_db_lock__:"
	dbMutationLockKeyPrefix  = "__thinkgo_db_mutation__:"
	dbMutationLockKey        = dbMutationLockKeyPrefix + "global"
	dbMutationLockTTL        = 5 * time.Minute
	dbMutationLockRenewEvery = dbMutationLockTTL / 3
	dbMutationLockWait       = 2 * time.Second
	dbMutationLockRetry      = 5 * time.Millisecond
)

var cacheTablePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// DB 是显式传播 SQL 错误并保留毫秒 TTL 的数据库缓存驱动。
type DB struct {
	conn       db.Connection
	table      string
	lockMu     sync.Mutex
	mutationMu sync.Mutex
}

// NewDB 验证连接与缓存表名后创建驱动。
func NewDB(conn db.Connection, table string) (*DB, error) {
	if conn == nil {
		return nil, ErrInvalidCacheDatabase
	}
	if !cacheTablePattern.MatchString(table) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidCacheTable, table)
	}
	return &DB{conn: conn, table: table}, nil
}

// CacheResourceIdentity 返回数据库连接与缓存表组成的稳定资源标识，阻止同一管理器注册重复 namespace。
func (c *DB) CacheResourceIdentity() string {
	if c == nil || c.conn == nil || c.table == "" {
		return ""
	}
	connectionID := c.conn.ConnectionID()
	if connectionID == "" {
		return ""
	}
	return fmt.Sprintf("db:%s:%s", connectionID, c.table)
}

// EnsureTable 确保缓存表存在，并按底层 SQL 方言生成安全建表语句。
func (c *DB) EnsureTable() error {
	if c == nil || c.conn == nil {
		return ErrInvalidCacheDatabase
	}
	raw, ok := c.conn.(db.RawQueryable)
	if !ok {
		return fmt.Errorf("%w: 连接不支持原生执行", ErrInvalidCacheDatabase)
	}
	_, err := raw.Execute(c.createTableSQL())
	return err
}

func (c *DB) createTableSQL() string {
	quote := c.quoteIdentifier
	table := quote(c.table)
	keyColumn := quote("key")
	valueColumn := quote("value")
	expiryColumn := quote("expiry")

	if sqlConn, ok := c.conn.(*db.SQLConnection); ok && sqlConn.Builder != nil {
		switch fmt.Sprintf("%T", sqlConn.Builder) {
		case "*builder.Sqlsrv":
			return fmt.Sprintf(
				"IF OBJECT_ID(N'%s', N'U') IS NULL BEGIN CREATE TABLE %s (%s NVARCHAR(255) NOT NULL PRIMARY KEY, %s NVARCHAR(MAX) NOT NULL, %s BIGINT NOT NULL DEFAULT 0) END",
				c.table, table, keyColumn, valueColumn, expiryColumn,
			)
		case "*builder.Oracle":
			return fmt.Sprintf(
				"BEGIN EXECUTE IMMEDIATE 'CREATE TABLE %s (%s VARCHAR2(255) PRIMARY KEY, %s CLOB NOT NULL, %s NUMBER(19) DEFAULT 0 NOT NULL)'; EXCEPTION WHEN OTHERS THEN IF SQLCODE != -955 THEN RAISE; END IF; END;",
				table, keyColumn, valueColumn, expiryColumn,
			)
		}
	}

	return fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s (%s VARCHAR(255) PRIMARY KEY, %s TEXT NOT NULL, %s BIGINT NOT NULL DEFAULT 0)",
		table, keyColumn, valueColumn, expiryColumn,
	)
}

func (c *DB) quoteIdentifier(name string) string {
	if sqlConn, ok := c.conn.(*db.SQLConnection); ok && sqlConn.Builder != nil {
		return sqlConn.Builder.QuoteIdentifier(name)
	}
	return "`" + name + "`"
}

func (c *DB) Get(key string) (interface{}, bool, error) {
	value, found, _, err := c.getStored(key, false)
	return value, found, err
}

func (c *DB) Set(key string, value interface{}, ttl time.Duration) error {
	if err := validateDriverTTL(ttl); err != nil {
		return err
	}
	expiry := int64(0)
	if ttl > 0 {
		expiry = time.Now().Add(ttl).UnixMilli()
	}
	return c.withMutationLock(func() error {
		return c.writeStored(key, value, expiry)
	})
}

func (c *DB) Has(key string) (bool, error) {
	_, found, err := c.Get(key)
	return found, err
}

func (c *DB) Delete(key string) error {
	if err := c.validateKey(key); err != nil {
		return err
	}
	return c.withMutationLock(func() error {
		_, err := db.NewDB(c.conn).Table(c.table).Where("key", key).Delete()
		return err
	})
}

func (c *DB) Clear() error {
	if c == nil || c.conn == nil {
		return ErrInvalidCacheDatabase
	}
	return c.withMutationLock(func() error {
		condition := fmt.Sprintf("%s NOT LIKE ?", c.quoteIdentifier("key"))
		_, err := db.NewDB(c.conn).Table(c.table).WhereRaw(condition, dbLockStoragePrefix+"%").Delete()
		return err
	})
}

// AcquireLock 使用缓存表内的条件更新实现跨进程租约获取，不依赖数据库方言专用锁语法。
func (c *DB) AcquireLock(key string, owner string, ttl time.Duration) (bool, error) {
	if c == nil || c.conn == nil || owner == "" || ttl <= 0 {
		return false, ErrInvalidCacheLock
	}
	ownerValue, err := dbLockOwnerValue(owner)
	if err != nil {
		return false, err
	}
	now := time.Now().UnixMilli()
	data := map[string]interface{}{
		"key":    dbLockStorageKey(key),
		"value":  ownerValue,
		"expiry": time.Now().Add(ttl).UnixMilli(),
	}
	c.lockMu.Lock()
	defer c.lockMu.Unlock()
	for attempt := 0; attempt < dbLockAcquireAttempts; attempt++ {
		query := db.NewDB(c.conn).Table(c.table).Where("key", data["key"])
		if _, insertErr := db.NewDB(c.conn).Table(c.table).Insert(data); insertErr == nil {
			return true, nil
		} else {
			existing, findErr := query.Find()
			if findErr != nil {
				return false, errors.Join(insertErr, findErr)
			}
			if existing == nil {
				if attempt+1 < dbLockAcquireAttempts {
					continue
				}
				return false, insertErr
			}
		}
		updated, updateErr := query.Where("expiry", "<=", now).Update(data)
		if updateErr != nil {
			return false, updateErr
		}
		return updated > 0, nil
	}
	return false, nil
}

// ReleaseLock 仅允许未过期且 owner 匹配的租约被删除，避免旧 owner 删除新租约。
func (c *DB) ReleaseLock(key string, owner string) (bool, error) {
	if c == nil || c.conn == nil || owner == "" {
		return false, ErrInvalidCacheLock
	}
	ownerValue, err := dbLockOwnerValue(owner)
	if err != nil {
		return false, err
	}
	c.lockMu.Lock()
	defer c.lockMu.Unlock()
	deleted, err := db.NewDB(c.conn).Table(c.table).
		Where("key", dbLockStorageKey(key)).
		Where("value", ownerValue).
		Where("expiry", ">", time.Now().UnixMilli()).
		Delete()
	return deleted > 0, err
}

// RenewLock 仅在原 owner 仍持有未过期租约时条件更新过期时间。
func (c *DB) RenewLock(key string, owner string, ttl time.Duration) (bool, error) {
	if c == nil || c.conn == nil || owner == "" || ttl <= 0 {
		return false, ErrInvalidCacheLock
	}
	ownerValue, err := dbLockOwnerValue(owner)
	if err != nil {
		return false, err
	}
	data := map[string]interface{}{
		"expiry": time.Now().Add(ttl).UnixMilli(),
	}
	c.lockMu.Lock()
	defer c.lockMu.Unlock()
	updated, err := db.NewDB(c.conn).Table(c.table).
		Where("key", dbLockStorageKey(key)).
		Where("value", ownerValue).
		Where("expiry", ">", time.Now().UnixMilli()).
		Update(data)
	return updated > 0, err
}

// withMutationLock 将所有表内修改统一纳入全局租约，避免 Clear 与按键写入交错。
func (c *DB) withMutationLock(callback func() error) (resultErr error) {
	if c == nil || c.conn == nil {
		return ErrInvalidCacheDatabase
	}
	if callback == nil {
		return ErrInvalidCacheLock
	}
	owner, err := newDBLockOwner()
	if err != nil {
		return err
	}
	acquired, err := acquireDBMutationLock(c, dbMutationLockKey, owner)
	if err != nil {
		return err
	}
	if !acquired {
		return ErrCacheLockBusy
	}
	defer func() {
		released, releaseErr := c.ReleaseLock(dbMutationLockKey, owner)
		if releaseErr == nil && !released {
			releaseErr = ErrCacheLockLost
		}
		resultErr = errors.Join(resultErr, releaseErr)
	}()
	renewal := startDBMutationLockRenewal(c, dbMutationLockKey, owner)
	defer func() {
		resultErr = errors.Join(resultErr, renewal.stop())
	}()
	c.mutationMu.Lock()
	defer c.mutationMu.Unlock()
	return callback()
}

type dbMutationLockRenewal struct {
	stopOnce sync.Once
	stopCh   chan struct{}
	done     chan error
	stopErr  error
}

// startDBMutationLockRenewal 在数据库修改临界区内按固定周期续租全局表锁。
func startDBMutationLockRenewal(c *DB, key, owner string) *dbMutationLockRenewal {
	session := &dbMutationLockRenewal{
		stopCh: make(chan struct{}),
		done:   make(chan error, 1),
	}
	go func() {
		ticker := time.NewTicker(dbMutationLockRenewEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				renewed, err := c.RenewLock(key, owner, dbMutationLockTTL)
				if err != nil {
					session.done <- err
					return
				}
				if !renewed {
					session.done <- ErrCacheLockLost
					return
				}
			case <-session.stopCh:
				session.done <- nil
				return
			}
		}
	}()
	return session
}

// stop 停止数据库租约续期并等待最后一次数据库请求完成。
func (s *dbMutationLockRenewal) stop() error {
	if s == nil {
		return nil
	}
	s.stopOnce.Do(func() {
		close(s.stopCh)
		s.stopErr = <-s.done
	})
	return s.stopErr
}

// acquireDBMutationLock 在有限窗口内等待表内修改锁，避免同一 key 的并发读改写丢失更新。
func acquireDBMutationLock(c *DB, key, owner string) (bool, error) {
	deadline := time.Now().Add(dbMutationLockWait)
	for {
		acquired, err := c.AcquireLock(key, owner, dbMutationLockTTL)
		if err != nil || acquired {
			return acquired, err
		}
		if !time.Now().Before(deadline) {
			return false, nil
		}
		time.Sleep(dbMutationLockRetry)
	}
}

// newDBLockOwner 为数据库缓存内部租约生成不可预测 owner，避免不同进程复用身份。
func newDBLockOwner() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidCacheLock, err)
	}
	return "db:" + hex.EncodeToString(random), nil
}

func dbLockOwnerValue(owner string) (string, error) {
	encoded, err := json.Marshal(owner)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidCacheLock, err)
	}
	return string(encoded), nil
}

func dbLockStorageKey(key string) string {
	digest := sha256.Sum256([]byte(key))
	return dbLockStoragePrefix + hex.EncodeToString(digest[:])
}

func (c *DB) Inc(key string, step int64) (int64, error) {
	return c.changeCounter(key, step, false)
}

func (c *DB) Dec(key string, step int64) (int64, error) {
	return c.changeCounter(key, step, true)
}

func (c *DB) changeCounter(key string, step int64, subtract bool) (int64, error) {
	if c == nil || c.conn == nil {
		return 0, ErrInvalidCacheDatabase
	}
	if err := validateCounterStep(step); err != nil {
		return 0, err
	}
	value := int64(0)
	resultErr := c.withMutationLock(func() error {
		storedValue, found, expiry, err := c.getStored(key, true)
		if err != nil {
			return err
		}
		current := int64(0)
		if found {
			current, err = strictCounterValue(storedValue)
			if err != nil {
				return err
			}
		}
		if subtract {
			value, err = checkedCounterSubtract(current, step)
			if err != nil {
				return err
			}
		} else {
			value, err = checkedCounterAdd(current, step)
			if err != nil {
				return err
			}
		}
		return c.writeStored(key, value, expiry)
	})
	return value, resultErr
}

func (c *DB) getStored(key string, preserveNumber bool) (interface{}, bool, int64, error) {
	if err := c.validateKey(key); err != nil {
		return nil, false, 0, err
	}
	result, err := db.NewDB(c.conn).Table(c.table).Where("key", key).Find()
	if err != nil || result == nil {
		return nil, false, 0, err
	}
	expiry, err := parseDBExpiry(result["expiry"])
	if err != nil {
		return nil, false, 0, err
	}
	if cacheExpiryReached(expiry, time.Now()) {
		// 过期读取与并发刷新可能交错，必须按读取到的旧 expiry 条件删除。
		if deleteErr := c.deleteExpiredVersion(key, result["expiry"]); deleteErr != nil {
			return nil, false, 0, deleteErr
		}
		return nil, false, 0, nil
	}
	encoded, err := databaseCacheBytes(result["value"])
	if err != nil {
		return nil, false, 0, err
	}
	var value interface{}
	if preserveNumber {
		value, err = decodeCounterJSON(encoded)
	} else {
		err = json.Unmarshal(encoded, &value)
	}
	if err != nil {
		return nil, false, 0, fmt.Errorf("%w: %v", ErrCorruptCacheEntry, err)
	}
	return value, true, expiry, nil
}

func (c *DB) deleteExpiredVersion(key string, rawExpiry interface{}) error {
	if err := c.validateKey(key); err != nil {
		return err
	}
	_, err := db.NewDB(c.conn).
		Table(c.table).
		Where("key", key).
		Where("expiry", rawExpiry).
		Delete()
	return err
}

func (c *DB) writeStored(key string, value interface{}, expiry int64) error {
	if err := c.validateKey(key); err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	data := map[string]interface{}{"key": key, "value": string(encoded), "expiry": expiry}
	var lastInsertErr error
	for attempt := 0; attempt < dbUpsertAttempts; attempt++ {
		updated, updateErr := db.NewDB(c.conn).Table(c.table).Where("key", key).Update(data)
		if updateErr != nil {
			return updateErr
		}
		if updated > 0 {
			return nil
		}

		// 某些数据库在值未变化时返回 0，但记录实际已匹配；先确认存在再决定插入。
		existing, findErr := db.NewDB(c.conn).Table(c.table).Where("key", key).Find()
		if findErr != nil {
			return findErr
		}
		if existing != nil {
			return nil
		}

		if _, insertErr := db.NewDB(c.conn).Table(c.table).Insert(data); insertErr == nil {
			return nil
		} else {
			lastInsertErr = insertErr
		}
	}
	return lastInsertErr
}

func (c *DB) validateKey(key string) error {
	if c == nil || c.conn == nil {
		return ErrInvalidCacheDatabase
	}
	if len(key) > maxDBCacheKeyBytes {
		return fmt.Errorf("%w: %d", ErrCacheKeyTooLong, len(key))
	}
	return nil
}

func parseDBExpiry(raw interface{}) (int64, error) {
	switch typed := raw.(type) {
	case nil:
		return 0, nil
	case string:
		value, err := strconv.ParseInt(typed, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%w: expiry", ErrCorruptCacheEntry)
		}
		return validateDBExpiry(value)
	case []byte:
		return parseDBExpiry(string(typed))
	default:
		value, err := strictCounterValue(raw)
		if err != nil {
			return 0, fmt.Errorf("%w: expiry: %v", ErrCorruptCacheEntry, err)
		}
		return validateDBExpiry(value)
	}
}

func validateDBExpiry(value int64) (int64, error) {
	if value < 0 {
		return 0, fmt.Errorf("%w: expiry 不能为负数", ErrCorruptCacheEntry)
	}
	return value, nil
}

func databaseCacheBytes(raw interface{}) ([]byte, error) {
	switch typed := raw.(type) {
	case string:
		return []byte(typed), nil
	case []byte:
		return append([]byte(nil), typed...), nil
	default:
		return nil, fmt.Errorf("%w: value 类型 %T", ErrCorruptCacheEntry, raw)
	}
}

func cacheExpiryReached(expiry int64, now time.Time) bool {
	if expiry <= 0 {
		return false
	}
	if expiry < millisecondEpochBoundary {
		return !now.Before(time.Unix(expiry, 0))
	}
	return !now.Before(time.UnixMilli(expiry))
}
