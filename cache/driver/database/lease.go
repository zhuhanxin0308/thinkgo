package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/zhuhanxin0308/thinkgo/framework/cache/contract"
	"github.com/zhuhanxin0308/thinkgo/framework/db"
)

// 临时值只存在于未提交事务，且与任何合法 JSON 字符串 owner 不同，保证影响行数可判断。
const dbLeaseCommitMarker = "null"

var errDatabaseLeaseChanged = errors.New("数据库缓存提交租约已变更")

// UpdateIfLockOwnerContext 用条件更新锁住租约行，在同一事务中提交数据并恢复 owner。
// 接管、续租和释放都需要同一行的写锁，因此不会在校验与提交之间更换持有者。
func (c *DB) UpdateIfLockOwnerContext(ctx context.Context, key string, ttl time.Duration, lockKey, owner string, update func(interface{}, bool) (interface{}, bool, error)) (bool, error) {
	if c == nil || c.conn == nil || ctx == nil || lockKey == "" || owner == "" {
		return false, ErrInvalidCacheLock
	}
	if update == nil {
		return false, contract.ErrNilAtomicUpdate
	}
	if err := validateDriverTTL(ttl); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	physicalKey, err := c.storageKey(key)
	if err != nil {
		return false, err
	}
	ownerValue, err := dbLockOwnerValue(owner)
	if err != nil {
		return false, err
	}
	err = db.NewDB(c.conn).TransactionContext(ctx, func(transaction *db.Tx) error {
		lease := transaction.Table(c.table).Where("key", dbLockStorageKey(lockKey))
		guard := lease
		if connection, ok := c.conn.(*db.SQLConnection); ok && connection.Builder != nil && connection.Builder.DialectName() == "oracle" {
			guard = guard.WhereRaw(fmt.Sprintf("DBMS_LOB.COMPARE(%s, TO_CLOB(?)) = 0", c.quoteIdentifier("value")), ownerValue)
		} else {
			guard = guard.Where("value", ownerValue)
		}
		matched, err := guard.Where("expiry", ">", time.Now().UnixMilli()).Update(map[string]interface{}{"value": dbLeaseCommitMarker})
		if err != nil {
			return err
		}
		if matched != 1 {
			return errDatabaseLeaseChanged
		}
		if err := c.updateLeasedRecord(transaction, physicalKey, ttl, update); err != nil {
			return err
		}
		_, err = lease.Update(map[string]interface{}{"value": ownerValue})
		return err
	})
	if errors.Is(err, errDatabaseLeaseChanged) {
		return false, nil
	}
	return err == nil, err
}

func (c *DB) updateLeasedRecord(transaction *db.Tx, key string, ttl time.Duration, update func(interface{}, bool) (interface{}, bool, error)) error {
	query := transaction.Table(c.table).Where("key", key)
	row, err := query.Find()
	if err != nil {
		return err
	}
	found := row != nil
	var current interface{}
	if found {
		expiry, err := parseDBExpiry(row["expiry"])
		if err != nil {
			return err
		}
		found = !cacheExpiryReached(expiry, time.Now())
		if found {
			encoded, err := databaseCacheBytes(row["value"])
			if err != nil {
				return err
			}
			if err := json.Unmarshal(encoded, &current); err != nil {
				return fmt.Errorf("%w: %v", ErrCorruptCacheEntry, err)
			}
		}
	}
	next, remove, err := update(current, found)
	if err != nil {
		return err
	}
	if remove {
		_, err := query.Delete()
		return err
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		return err
	}
	expiry := int64(0)
	if ttl > 0 {
		expiry = time.Now().Add(ttl).UnixMilli()
	}
	data := map[string]interface{}{"key": key, "value": string(encoded), "expiry": expiry}
	if row != nil {
		_, err = query.Update(data)
	} else {
		_, err = transaction.Table(c.table).Insert(data)
	}
	return err
}
