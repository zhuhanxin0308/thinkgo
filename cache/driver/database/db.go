package database

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

const (
	maxDBCacheKeyBytes       = 255
	millisecondEpochBoundary = int64(1_000_000_000_000)
	dbUpsertAttempts         = 2
	dbLockAcquireAttempts    = 3
	dbLockStoragePrefix      = "__thinkgo_db_lock__:"
)

var cacheTablePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// DB 是显式传播 SQL 错误并保留毫秒 TTL 的数据库缓存驱动。
type DB struct {
	conn  db.Connection
	table string
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
		switch sqlConn.Builder.DialectName() {
		case "sqlserver":
			return fmt.Sprintf(
				"IF OBJECT_ID(N'%s', N'U') IS NULL BEGIN CREATE TABLE %s (%s NVARCHAR(255) NOT NULL PRIMARY KEY, %s NVARCHAR(MAX) NOT NULL, %s BIGINT NOT NULL DEFAULT 0) END",
				c.table, table, keyColumn, valueColumn, expiryColumn,
			)
		case "oracle":
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
	value, found, _, err := c.getStored(key)
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
	physicalKey, err := c.storageKey(key)
	if err != nil {
		return err
	}
	return c.writeStored(physicalKey, value, expiry)
}

func (c *DB) Has(key string) (bool, error) {
	_, found, err := c.Get(key)
	return found, err
}

func (c *DB) Delete(key string) error {
	physicalKey, err := c.storageKey(key)
	if err != nil {
		return err
	}
	_, err = db.NewDB(c.conn).Table(c.table).Where("key", physicalKey).Delete()
	return err
}

func (c *DB) Clear() error {
	return c.clearGeneration()
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
	updated, err := db.NewDB(c.conn).Table(c.table).
		Where("key", dbLockStorageKey(key)).
		Where("value", ownerValue).
		Where("expiry", ">", time.Now().UnixMilli()).
		Update(data)
	return updated > 0, err
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
	if err := validateCounterStep(step); err != nil {
		return 0, err
	}
	physicalKey, err := c.storageKey(key)
	if err != nil {
		return 0, err
	}
	deadline := time.Now().Add(dbCompareWait)
	for attempt := 0; attempt < dbCompareAttempts && time.Now().Before(deadline); attempt++ {
		row, expiry, err := c.readStoredRecord(physicalKey)
		if err != nil {
			return 0, err
		}
		current := int64(0)
		var previous []byte
		if row != nil {
			previous, err = databaseCacheBytes(row["value"])
			if err != nil {
				return 0, err
			}
			storedValue, err := decodeCounterJSON(previous)
			if err != nil {
				return 0, fmt.Errorf("%w: %v", ErrCorruptCacheEntry, err)
			}
			current, err = strictCounterValue(storedValue)
			if err != nil {
				return 0, err
			}
			if step == 0 {
				return current, nil
			}
		}
		var value int64
		if subtract {
			value, err = checkedCounterSubtract(current, step)
		} else {
			value, err = checkedCounterAdd(current, step)
		}
		if err != nil {
			return 0, err
		}
		encoded := strconv.FormatInt(value, 10)
		if row == nil {
			_, insertErr := db.NewDB(c.conn).Table(c.table).Insert(map[string]interface{}{"key": physicalKey, "value": encoded, "expiry": expiry})
			if insertErr == nil {
				return value, nil
			}
			// 仅在另一写入已经创建同一行时重试，真实后端错误继续传播。
			existing, _, readErr := c.readStoredRecord(physicalKey)
			if readErr != nil || existing == nil {
				return 0, errors.Join(insertErr, readErr)
			}
			continue
		}
		updated, updateErr := c.compareValue(physicalKey, string(previous)).Where("expiry", row["expiry"]).Update(map[string]interface{}{"value": encoded})
		if updateErr != nil {
			return 0, updateErr
		}
		if updated > 0 {
			return value, nil
		}
	}
	return 0, ErrCacheLockBusy
}

func (c *DB) getStored(key string) (interface{}, bool, int64, error) {
	physicalKey, err := c.storageKey(key)
	if err != nil {
		return nil, false, 0, err
	}
	result, expiry, err := c.readStoredRecord(physicalKey)
	if err != nil || result == nil {
		return nil, false, 0, err
	}
	encoded, err := databaseCacheBytes(result["value"])
	if err != nil {
		return nil, false, 0, err
	}
	var value interface{}
	err = json.Unmarshal(encoded, &value)
	if err != nil {
		return nil, false, 0, fmt.Errorf("%w: %v", ErrCorruptCacheEntry, err)
	}
	return value, true, expiry, nil
}

func (c *DB) readStoredRecord(key string) (map[string]interface{}, int64, error) {
	if err := c.validateKey(key); err != nil {
		return nil, 0, err
	}
	result, err := db.NewDB(c.conn).Table(c.table).Where("key", key).Find()
	if err != nil || result == nil {
		return nil, 0, err
	}
	expiry, err := parseDBExpiry(result["expiry"])
	if err != nil {
		return nil, 0, err
	}
	if cacheExpiryReached(expiry, time.Now()) {
		// 过期读取与并发刷新可能交错，必须按读取到的旧 expiry 条件删除。
		if deleteErr := c.deleteExpiredVersion(key, result["expiry"]); deleteErr != nil {
			return nil, 0, deleteErr
		}
		return nil, 0, nil
	}
	return result, expiry, nil
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
