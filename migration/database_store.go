package migration

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zhuhanxin0308/thinkgo/framework/db"
)

const (
	defaultMigrationLockTimeout  = 30 * time.Second
	migrationCleanupTimeout      = 5 * time.Second
	sqliteMigrationSavepoint     = "thinkgo_internal_migration_step"
	migrationFailureOperation    = "operation_failed"
	migrationFailureFinalization = "finalization_failed"
	migrationFenceLockName       = "global"
)

// DatabaseStore 使用固定 SQL 会话、方言锁和 durable journal 持久化迁移状态。
type DatabaseStore struct {
	database     *db.DB
	dialect      string
	executor     Executor
	session      db.PinnedSQLConnection
	fencingToken int64
	sqliteLocked bool
	schemaMu     sync.Mutex
}

// NewDatabaseStore 为受支持的 SQL 方言创建迁移存储。
func NewDatabaseStore(database *db.DB) (*DatabaseStore, error) {
	if database == nil {
		return nil, fmt.Errorf("%w: 数据库为空", ErrInvalidMigration)
	}
	dialect := database.DialectName()
	if _, _, err := migrationTableStatements(dialect); err != nil {
		return nil, err
	}
	return &DatabaseStore{database: database, dialect: dialect}, nil
}

// Ensure 并发安全地确保迁移历史表存在，并在创建竞争后复查真实状态。
func (store *DatabaseStore) Ensure(ctx context.Context) error {
	if store == nil || store.database == nil || ctx == nil {
		return fmt.Errorf("%w: 迁移存储或上下文为空", ErrInvalidMigration)
	}
	store.schemaMu.Lock()
	defer store.schemaMu.Unlock()
	definitions, err := migrationTableDefinitions(store.dialect)
	if err != nil {
		return err
	}
	for _, definition := range definitions {
		exists, existsErr := store.tableExists(ctx, definition)
		if existsErr != nil {
			return existsErr
		}
		if !exists {
			if _, createErr := store.activeExecutor().ExecuteContext(ctx, definition.createSQL); createErr != nil {
				exists, checkErr := store.tableExists(ctx, definition)
				if checkErr != nil || !exists {
					return fmt.Errorf("创建迁移表 %s 失败: %w", definition.name, createErr)
				}
			}
		}
	}
	return nil
}

func (store *DatabaseStore) tableExists(ctx context.Context, definition migrationTableDefinition) (bool, error) {
	name := definition.name
	if store.dialect == "oracle" {
		name = strings.ToUpper(name)
	}
	rows, err := store.activeExecutor().QueryContext(ctx, definition.existsSQL, name)
	if err != nil {
		return false, fmt.Errorf("检查迁移表 %s 失败: %w", definition.name, err)
	}
	return len(rows) > 0, nil
}

// Inspect 只读检查迁移历史表；表不存在时返回 initialized=false，不创建任何对象。
func (store *DatabaseStore) Inspect(ctx context.Context) ([]History, bool, error) {
	if store == nil || store.database == nil || ctx == nil {
		return nil, false, fmt.Errorf("%w: 迁移存储或上下文为空", ErrInvalidMigration)
	}
	definitions, err := migrationTableDefinitions(store.dialect)
	if err != nil {
		return nil, false, err
	}
	exists, err := store.tableExists(ctx, definitions[0])
	if err != nil || !exists {
		return nil, false, err
	}
	history, err := store.Applied(ctx)
	return history, true, err
}

// Applied 返回按批次和名称升序排列的完整迁移历史。
func (store *DatabaseStore) Applied(ctx context.Context) ([]History, error) {
	if store == nil || store.database == nil || ctx == nil {
		return nil, fmt.Errorf("%w: 迁移存储或上下文为空", ErrInvalidMigration)
	}
	rows, err := store.activeExecutor().QueryContext(
		ctx,
		"SELECT name, checksum, batch, applied_at FROM "+migrationHistoryTable+" ORDER BY batch ASC, name ASC",
	)
	if err != nil {
		return nil, err
	}
	history := make([]History, 0, len(rows))
	for index, row := range rows {
		name, nameErr := migrationText(migrationRowValue(row, "name"))
		checksum, checksumErr := migrationText(migrationRowValue(row, "checksum"))
		batch, batchErr := migrationInt64(migrationRowValue(row, "batch"))
		appliedAt, appliedAtErr := migrationText(migrationRowValue(row, "applied_at"))
		if nameErr != nil || checksumErr != nil || batchErr != nil || appliedAtErr != nil {
			return nil, fmt.Errorf("%w: 第 %d 条迁移历史字段非法", ErrMigrationDrift, index+1)
		}
		history = append(history, History{Name: name, Checksum: checksum, Batch: batch, AppliedAt: appliedAt})
	}
	return history, nil
}

// Apply 在逐迁移事务中执行 Up，并仅在成功后写入历史。
func (store *DatabaseStore) Apply(ctx context.Context, current Migration, batch int64) error {
	if store == nil || store.database == nil || ctx == nil || isNilMigration(current) || batch <= 0 {
		return fmt.Errorf("%w: 应用迁移参数非法", ErrInvalidMigration)
	}
	if !store.locked() {
		return ErrMigrationLockRequired
	}
	if err := store.recordApplying(ctx, current, batch, JournalOperationApply); err != nil {
		return err
	}
	operationErr := store.applyMigration(ctx, current, batch)
	if operationErr == nil {
		return nil
	}
	failureCode := migrationFailureOperation
	if errors.Is(operationErr, ErrMigrationLockLost) {
		failureCode = migrationFailureFinalization
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), migrationCleanupTimeout)
	defer cancel()
	return errors.Join(operationErr, store.recordFailed(cleanupCtx, current.Name(), failureCode))
}

// Revert 在逐迁移事务中执行 Down，并仅在成功后删除历史。
func (store *DatabaseStore) Revert(ctx context.Context, current Migration) error {
	if store == nil || store.database == nil || ctx == nil || isNilMigration(current) {
		return fmt.Errorf("%w: 回滚迁移参数非法", ErrInvalidMigration)
	}
	if !store.locked() {
		return ErrMigrationLockRequired
	}
	history, err := store.historyByName(ctx, current.Name())
	if err != nil {
		return err
	}
	if history.Checksum != current.Checksum() {
		return fmt.Errorf("%w: %s", ErrMigrationDrift, current.Name())
	}
	if err := store.recordApplying(ctx, current, history.Batch, JournalOperationRevert); err != nil {
		return err
	}
	operationErr := store.revertMigration(ctx, current)
	if operationErr == nil {
		return nil
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), migrationCleanupTimeout)
	defer cancel()
	return errors.Join(operationErr, store.recordFailed(cleanupCtx, current.Name(), migrationFailureOperation))
}

func (store *DatabaseStore) activeExecutor() Executor {
	if store.executor != nil {
		return store.executor
	}
	return store.database
}

func (store *DatabaseStore) locked() bool {
	return store != nil && store.session != nil && store.fencingToken > 0
}

func migrationText(value interface{}) (string, error) {
	switch typed := value.(type) {
	case string:
		if typed == "" {
			return "", ErrMigrationDrift
		}
		return typed, nil
	case []byte:
		if len(typed) == 0 {
			return "", ErrMigrationDrift
		}
		return string(typed), nil
	case time.Time:
		return typed.UTC().Format(time.RFC3339Nano), nil
	default:
		if value == nil {
			return "", ErrMigrationDrift
		}
		text := fmt.Sprint(value)
		if text == "" {
			return "", ErrMigrationDrift
		}
		return text, nil
	}
}

func migrationRowValue(row map[string]interface{}, column string) interface{} {
	if value, exists := row[column]; exists {
		return value
	}
	for name, value := range row {
		if strings.EqualFold(strings.TrimSpace(name), column) {
			return value
		}
	}
	return nil
}

func migrationInt64(value interface{}) (int64, error) {
	if value == nil {
		return 0, ErrMigrationDrift
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return reflected.Int(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		unsigned := reflected.Uint()
		if unsigned > uint64(math.MaxInt64) {
			return 0, ErrMigrationDrift
		}
		return int64(unsigned), nil
	case reflect.Float32, reflect.Float64:
		value := reflected.Float()
		if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value || value < math.MinInt64 || value >= float64(math.MaxInt64) {
			return 0, ErrMigrationDrift
		}
		return int64(value), nil
	case reflect.String:
		return strconv.ParseInt(reflected.String(), 10, 64)
	case reflect.Slice:
		if bytes, ok := value.([]byte); ok {
			return strconv.ParseInt(string(bytes), 10, 64)
		}
	}
	return 0, ErrMigrationDrift
}
