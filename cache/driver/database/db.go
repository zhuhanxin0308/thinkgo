package database

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
	"github.com/zhuhanxin0308/thinkgo/v3/internal/cachekey"
)

const (
	maxDBCacheKeyBytes       = cachekey.MaxLogicalKeyBytes
	maxDBPhysicalKeyBytes    = 255
	millisecondEpochBoundary = int64(1_000_000_000_000)
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
	expiry := dbExpiryMillis(time.Now(), ttl)
	physicalKey, err := c.storageKey(key)
	if err != nil {
		return err
	}
	if err := c.writeStored(physicalKey, value, expiry); err != nil {
		return err
	}
	return c.reclaimObsoleteWrite(physicalKey)
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
	physical := dbLockStorageKey(key)
	table := db.NewDB(c.conn).Table(c.table)
	for attempt := 0; attempt < dbLockAcquireAttempts; attempt++ {
		now := time.Now()
		data := map[string]interface{}{"key": physical, "value": ownerValue, "expiry": dbExpiryMillis(now, ttl)}
		query := table.Where("key", physical)
		insertErr := requireCacheMutation(table.Insert(data))
		if insertErr == nil {
			return true, nil
		}
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
		updated, updateErr := query.Where("expiry", "<=", now.UnixMilli()).Update(data)
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
	deleted, err := c.compareValue(dbLockStorageKey(key), ownerValue).
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
	now := time.Now()
	expiry := dbExpiryMillis(now, ttl)
	physical := dbLockStorageKey(key)
	updated, err := c.compareValue(physical, ownerValue).
		Where("expiry", ">", now.UnixMilli()).
		Update(map[string]interface{}{"expiry": expiry})
	if err != nil || updated > 0 {
		return updated > 0, err
	}
	// 只报告变化行的驱动会把续到相同刻度的更新计为零。点查后按字节验证持有者，并再次核对时钟。
	row, err := db.NewDB(c.conn).Table(c.table).Where("key", physical).Find()
	if err != nil {
		return false, err
	}
	return storedRecordMatches(row, []byte(ownerValue), expiry) && expiry > time.Now().UnixMilli(), nil
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
			data := map[string]interface{}{"key": physicalKey, "value": encoded, "expiry": expiry}
			insertErr := requireCacheMutation(db.NewDB(c.conn).Table(c.table).Insert(data))
			if insertErr == nil {
				return value, c.reclaimObsoleteWrite(physicalKey)
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
			return value, c.reclaimObsoleteWrite(physicalKey)
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
	value, err := decodeDatabaseCacheValue(result["value"])
	if err != nil {
		return nil, false, 0, err
	}
	return value, true, expiry, nil
}

func (c *DB) readStoredRecord(key string) (map[string]interface{}, int64, error) {
	if err := c.validateKeySize(len(key), maxDBPhysicalKeyBytes); err != nil {
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
	if err := c.validateKeySize(len(key), maxDBPhysicalKeyBytes); err != nil {
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
	if err := c.validateKeySize(len(key), maxDBPhysicalKeyBytes); err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	data := map[string]interface{}{"key": key, "value": string(encoded), "expiry": expiry}
	table := db.NewDB(c.conn).Table(c.table)
	query := table.Where("key", key)
	deadline := time.Now().Add(dbCompareWait)
	for attempt := 0; attempt < dbCompareAttempts && (attempt == 0 || time.Now().Before(deadline)); attempt++ {
		updated, updateErr := query.Update(data)
		if updateErr != nil {
			return updateErr
		}
		if updated > 0 {
			return nil
		}

		// 零影响行既可能是幂等写入，也可能是查询前刚有其他请求插入了不同值。
		existing, findErr := query.Find()
		if findErr != nil {
			return findErr
		}
		if existing != nil {
			if storedRecordMatches(existing, encoded, expiry) {
				return nil
			}
			continue
		}

		insertErr := requireCacheMutation(table.Insert(data))
		if insertErr == nil {
			return nil
		}
		// 插入冲突时重新尝试覆写；若目标行仍不存在，保留真正的后端错误。
		existing, findErr = query.Find()
		if findErr != nil || existing == nil {
			return errors.Join(insertErr, findErr)
		}
	}
	return ErrCacheLockBusy
}

// 需要改变记录的操作必须有影响行；触发器可以忽略写入并让驱动返回空错误。
func requireCacheMutation(affected int64, err error) error {
	if err == nil && affected <= 0 {
		return sql.ErrNoRows
	}
	return err
}

func storedRecordMatches(row map[string]interface{}, value []byte, expiry int64) bool {
	if row == nil {
		return false
	}
	stored, valueErr := databaseCacheBytes(row["value"])
	storedExpiry, expiryErr := parseDBExpiry(row["expiry"])
	return valueErr == nil && expiryErr == nil && bytes.Equal(stored, value) && storedExpiry == expiry
}

func (c *DB) validateKeySize(size, limit int) error {
	if c == nil || c.conn == nil {
		return ErrInvalidCacheDatabase
	}
	if size > limit {
		return fmt.Errorf("%w: %d", ErrCacheKeyTooLong, size)
	}
	return nil
}

// dbExpiryMillis 将绝对截止时刻向上取整到数据库的毫秒精度，避免正时长提前到期。
func dbExpiryMillis(now time.Time, ttl time.Duration) int64 {
	if ttl == 0 {
		return 0
	}
	deadline := now.Add(ttl)
	millis := deadline.UnixMilli()
	if deadline.Nanosecond()%int(time.Millisecond) != 0 {
		millis++
	}
	return millis
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

func decodeDatabaseCacheValue(raw interface{}) (interface{}, error) {
	encoded, err := databaseCacheBytes(raw)
	if err != nil {
		return nil, err
	}
	var value interface{}
	if err = json.Unmarshal(encoded, &value); err == nil {
		return value, nil
	}
	// 仅恢复旧解码器无法表示的顶层整数，保持已有数值类型和嵌套值的解码语义。
	literal := bytes.Trim(encoded, " \t\r\n")
	if len(literal) > 0 && (literal[0] == '-' || literal[0] >= '0' && literal[0] <= '9') &&
		!bytes.ContainsAny(literal, ".eE") && json.Valid(encoded) {
		return json.Number(literal), nil
	}
	return nil, fmt.Errorf("%w: %v", ErrCorruptCacheEntry, err)
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
