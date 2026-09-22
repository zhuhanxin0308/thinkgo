package migration

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

type migrationLockSessionProbe struct {
	dialect    string
	statements []string
	arguments  [][]interface{}
	results    map[string]interface{}
	errors     map[string]error
	invalid    bool
}

func (session *migrationLockSessionProbe) DialectName() string { return session.dialect }

func (session *migrationLockSessionProbe) Invalidate() { session.invalid = true }

func (session *migrationLockSessionProbe) TransactionContext(_ context.Context, callback func(db.ContextualRawQueryable) error) error {
	return callback(session)
}

func (session *migrationLockSessionProbe) QueryContext(_ context.Context, statement string, args ...interface{}) ([]map[string]interface{}, error) {
	session.statements = append(session.statements, statement)
	session.arguments = append(session.arguments, append([]interface{}(nil), args...))
	for fragment, queryErr := range session.errors {
		if strings.Contains(statement, fragment) {
			return nil, queryErr
		}
	}
	for fragment, value := range session.results {
		if strings.Contains(statement, fragment) {
			column := "lock_result"
			if strings.Contains(fragment, "DATABASE()") || strings.Contains(fragment, "SYS_CONTEXT") {
				column = "lock_scope"
			} else if strings.Contains(fragment, "PRAGMA busy_timeout") {
				column = "timeout"
			} else if strings.Contains(fragment, "SELECT fencing_token") {
				column = "fencing_token"
			}
			return []map[string]interface{}{{column: value}}, nil
		}
	}
	return []map[string]interface{}{{"lock_result": int64(0)}}, nil
}

func (session *migrationLockSessionProbe) ExecuteContext(_ context.Context, statement string, args ...interface{}) (int64, error) {
	session.statements = append(session.statements, statement)
	session.arguments = append(session.arguments, append([]interface{}(nil), args...))
	for fragment, executeErr := range session.errors {
		if strings.Contains(statement, fragment) {
			return 0, executeErr
		}
	}
	return 0, nil
}

// TestDialectMigrationLockerContracts 验证五个方言使用会话级锁并严格检查返回码。
func TestDialectMigrationLockerContracts(t *testing.T) {
	testCases := []struct {
		name            string
		dialect         string
		results         map[string]interface{}
		acquireContains string
		releaseContains string
	}{
		{
			name: "mysql", dialect: "mysql",
			results:         map[string]interface{}{"DATABASE()": "tenant_database", "GET_LOCK": int64(1), "RELEASE_LOCK": int64(1)},
			acquireContains: "GET_LOCK", releaseContains: "RELEASE_LOCK",
		},
		{
			name: "postgres", dialect: "postgres",
			results:         map[string]interface{}{"pg_advisory_lock": nil, "pg_advisory_unlock": true},
			acquireContains: "pg_advisory_lock", releaseContains: "pg_advisory_unlock",
		},
		{
			name: "sqlserver", dialect: "sqlserver",
			results:         map[string]interface{}{"sp_getapplock": int64(0), "sp_releaseapplock": int64(0)},
			acquireContains: "sp_getapplock", releaseContains: "sp_releaseapplock",
		},
		{
			name: "oracle", dialect: "oracle",
			results:         map[string]interface{}{"SYS_CONTEXT": "ORCL:APP", "DBMS_LOCK.REQUEST": int64(0), "DBMS_LOCK.RELEASE": int64(0)},
			acquireContains: "DBMS_LOCK.REQUEST", releaseContains: "DBMS_LOCK.RELEASE",
		},
		{
			name: "sqlite", dialect: "sqlite", results: map[string]interface{}{"PRAGMA busy_timeout": int64(5000)},
			acquireContains: "BEGIN IMMEDIATE", releaseContains: "COMMIT",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			session := &migrationLockSessionProbe{dialect: testCase.dialect, results: testCase.results}
			locker, err := newDialectMigrationLocker(session)
			if err != nil {
				t.Fatalf("创建方言锁失败: %v", err)
			}
			if err = locker.Acquire(context.Background(), 1500*time.Millisecond); err != nil {
				t.Fatalf("获取方言锁失败: %v", err)
			}
			if err = locker.Release(context.Background(), true); err != nil {
				t.Fatalf("释放方言锁失败: %v", err)
			}
			joined := strings.Join(session.statements, "\n")
			if !strings.Contains(joined, testCase.acquireContains) || !strings.Contains(joined, testCase.releaseContains) {
				t.Fatalf("方言锁 SQL 不完整: %s", joined)
			}
			if testCase.dialect == "oracle" {
				if strings.Contains(joined, "FALSE") || strings.Contains(joined, "DBMS_LOCK.X_MODE") {
					t.Fatalf("Oracle 锁 SQL 使用了 23c 前不可用的 SQL BOOLEAN 或包常量: %s", joined)
				}
				var requestArguments []interface{}
				for index, statement := range session.statements {
					if strings.Contains(statement, "DBMS_LOCK.REQUEST") {
						requestArguments = session.arguments[index]
						break
					}
				}
				if len(requestArguments) != 3 || requestArguments[1] != oracleExclusiveLockMode {
					t.Fatalf("Oracle 锁模式参数错误: %#v", requestArguments)
				}
			}
		})
	}
}

// TestDialectMigrationLockerRejectsFailureReturnCodes 验证超时、锁丢失和 Oracle 权限错误均 fail-closed。
func TestDialectMigrationLockerRejectsFailureReturnCodes(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		session *migrationLockSessionProbe
		want    error
	}{
		{
			name: "mysql_timeout",
			session: &migrationLockSessionProbe{dialect: "mysql", results: map[string]interface{}{
				"DATABASE()": "tenant_database", "GET_LOCK": int64(0),
			}},
			want: ErrMigrationLockUnavailable,
		},
		{
			name: "sqlserver_deadlock",
			session: &migrationLockSessionProbe{dialect: "sqlserver", results: map[string]interface{}{
				"sp_getapplock": int64(-3),
			}},
			want: ErrMigrationLockUnavailable,
		},
		{
			name: "oracle_privilege",
			session: &migrationLockSessionProbe{dialect: "oracle", results: map[string]interface{}{
				"SYS_CONTEXT": "ORCL:APP",
			}, errors: map[string]error{"DBMS_LOCK.REQUEST": errors.New("ORA-01031: insufficient privileges")}},
			want: ErrMigrationLockUnavailable,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			locker, err := newDialectMigrationLocker(testCase.session)
			if err != nil {
				t.Fatalf("创建方言锁失败: %v", err)
			}
			if err = locker.Acquire(context.Background(), time.Second); !errors.Is(err, testCase.want) {
				t.Fatalf("失败返回码没有关闭执行: %v", err)
			}
			if testCase.name == "oracle_privilege" && !strings.Contains(err.Error(), "EXECUTE DBMS_LOCK") {
				t.Fatalf("Oracle 权限错误缺少明确提示: %v", err)
			}
		})
	}
}

// TestDialectMigrationLockerDetectsReleaseFailure 验证解锁返回失败会标记锁已丢失。
func TestDialectMigrationLockerDetectsReleaseFailure(t *testing.T) {
	session := &migrationLockSessionProbe{dialect: "postgres", results: map[string]interface{}{
		"pg_advisory_lock": nil, "pg_advisory_unlock": false,
	}}
	locker, err := newDialectMigrationLocker(session)
	if err != nil {
		t.Fatalf("创建 PostgreSQL 迁移锁失败: %v", err)
	}
	if err = locker.Acquire(context.Background(), time.Second); err != nil {
		t.Fatalf("获取 PostgreSQL 迁移锁失败: %v", err)
	}
	if err = locker.Release(context.Background(), true); !errors.Is(err, ErrMigrationLockLost) {
		t.Fatalf("解锁失败没有报告锁丢失: %v", err)
	}
}

// TestDatabaseStoreReleaseFailureInvalidatesSessionAndJoinsPrimaryError 验证解锁失败会丢弃会话且不覆盖 callback 主错误。
func TestDatabaseStoreReleaseFailureInvalidatesSessionAndJoinsPrimaryError(t *testing.T) {
	primaryErr := errors.New("migration callback failed")
	session := &migrationLockSessionProbe{
		dialect: "postgres",
		results: map[string]interface{}{
			"pg_advisory_lock":     nil,
			"pg_advisory_unlock":   false,
			"SELECT fencing_token": int64(1),
		},
	}
	store := &DatabaseStore{database: db.NewDB(nil), dialect: "postgres"}
	err := store.withMigrationSession(context.Background(), session, func(Store) error {
		return primaryErr
	})
	if !errors.Is(err, primaryErr) || !errors.Is(err, ErrMigrationLockLost) {
		t.Fatalf("主错误或解锁错误丢失: %v", err)
	}
	if !session.invalid {
		t.Fatal("解锁失败后没有失效固定物理会话")
	}
}

// TestSQLiteMigrationLockerRollsBackOnUnsafeExit 验证 SQLite 可明确选择回滚整个 BEGIN IMMEDIATE 会话。
func TestSQLiteMigrationLockerRollsBackOnUnsafeExit(t *testing.T) {
	session := &migrationLockSessionProbe{dialect: "sqlite", results: map[string]interface{}{"PRAGMA busy_timeout": int64(5000)}}
	locker, err := newDialectMigrationLocker(session)
	if err != nil {
		t.Fatalf("创建 SQLite 迁移锁失败: %v", err)
	}
	if err = locker.Acquire(context.Background(), time.Second); err != nil {
		t.Fatalf("获取 SQLite 迁移锁失败: %v", err)
	}
	if err = locker.Release(context.Background(), false); err != nil {
		t.Fatalf("回滚 SQLite 迁移锁失败: %v", err)
	}
	if joined := strings.Join(session.statements, "\n"); !strings.Contains(joined, "ROLLBACK") || !strings.Contains(joined, "PRAGMA busy_timeout = 5000") {
		t.Fatalf("SQLite 不安全退出没有回滚并恢复 busy timeout: %s", joined)
	}
}

// TestMigrationLockParsersAndTimeoutBounds 验证驱动布尔表示和方言超时上界不会产生溢出。
func TestMigrationLockParsersAndTimeoutBounds(t *testing.T) {
	for _, value := range []interface{}{true, "true", "1", []byte("1"), int64(1)} {
		parsed, err := migrationLockBool([]map[string]interface{}{{"LOCK_RESULT": value}}, "lock_result")
		if err != nil || !parsed {
			t.Fatalf("锁布尔值解析失败: value=%#v parsed=%t err=%v", value, parsed, err)
		}
	}
	if parsed, err := migrationLockBool([]map[string]interface{}{{"lock_result": "false"}}, "lock_result"); err != nil || parsed {
		t.Fatalf("false 锁结果解析错误: parsed=%t err=%v", parsed, err)
	}
	if _, err := migrationLockValue(nil, "lock_result"); err == nil {
		t.Fatal("空锁结果必须被拒绝")
	}
	if seconds := durationCeilingSeconds(0); seconds != 1 {
		t.Fatalf("零秒锁等待没有安全上取整: %d", seconds)
	}
	if milliseconds := durationCeilingMilliseconds(1000000 * time.Hour); milliseconds != maximumSQLServerLockMillis {
		t.Fatalf("SQL Server 锁等待没有限制为 int32: %d", milliseconds)
	}
}

// TestSignedMigrationLockKeyPartPreservesUint32Bits 验证 PostgreSQL 双 int32
// 锁键使用明确的二进制补码映射，不依赖可能溢出的直接类型转换。
func TestSignedMigrationLockKeyPartPreservesUint32Bits(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		value uint32
		want  int32
	}{
		{name: "zero", value: 0, want: 0},
		{name: "maximum positive", value: uint32(math.MaxInt32), want: math.MaxInt32},
		{name: "minimum negative", value: uint32(math.MaxInt32) + 1, want: math.MinInt32},
		{name: "all bits set", value: math.MaxUint32, want: -1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := signedMigrationLockKeyPart(testCase.value); got != testCase.want {
				t.Fatalf("锁键补码映射错误: value=%d got=%d want=%d", testCase.value, got, testCase.want)
			}
		})
	}
}

// TestDialectMigrationLockerRejectsUnsupportedSession 验证空会话与未知方言不会静默降级为无锁执行。
func TestDialectMigrationLockerRejectsUnsupportedSession(t *testing.T) {
	if _, err := newDialectMigrationLocker(nil); !errors.Is(err, ErrInvalidMigration) {
		t.Fatalf("空迁移锁会话错误不稳定: %v", err)
	}
	session := &migrationLockSessionProbe{dialect: "unknown"}
	if _, err := newDialectMigrationLocker(session); !errors.Is(err, ErrInvalidMigration) {
		t.Fatalf("未知锁方言没有被拒绝: %v", err)
	}
}
