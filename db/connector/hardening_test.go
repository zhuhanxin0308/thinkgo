package connector

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

type connectorAvailableDriver struct{}

func (connectorAvailableDriver) Open(string) (driver.Conn, error) {
	return connectorAvailableConnection{}, nil
}

type connectorAvailableConnection struct{}

func (connectorAvailableConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("测试连接不支持预编译")
}
func (connectorAvailableConnection) Close() error { return nil }
func (connectorAvailableConnection) Begin() (driver.Tx, error) {
	return nil, errors.New("测试连接不支持事务")
}
func (connectorAvailableConnection) Ping(context.Context) error { return nil }

// TestNetworkConnectorValidationRejectsInvalidCoordinates 验证网络连接器不会把非法地址交给驱动。
func TestNetworkConnectorValidationRejectsInvalidCoordinates(t *testing.T) {
	cases := []db.Config{
		{Type: "mysql", Database: "app", Hostport: "3306"},
		{Type: "mysql", Database: "app", Hostname: "db host", Hostport: "3306"},
		{Type: "mysql", Database: "app", Hostname: "localhost", Hostport: "0"},
		{Type: "mysql", Database: "app", Hostname: "localhost", Hostport: "65536"},
		{Type: "mysql", Database: "app", Hostname: "localhost", Hostport: "not-a-port"},
	}
	for index, config := range cases {
		if _, err := validateConnectorConfig(config, "mysql", true, true); !errors.Is(err, db.ErrInvalidDatabaseConfig) {
			t.Fatalf("第 %d 个地址应返回 ErrInvalidDatabaseConfig，实际为 %v", index, err)
		}
	}
}

// TestVerifySQLConnectionClosesHandleOnInvalidBuilder 验证连接包装依赖非法时
// 已创建的 database/sql 句柄仍会被释放。
func TestVerifySQLConnectionClosesHandleOnInvalidBuilder(t *testing.T) {
	driverName := "thinkgo_connector_available_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	sql.Register(driverName, connectorAvailableDriver{})
	handle, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("创建测试 SQL 句柄失败: %v", err)
	}
	if _, err := verifySQLConnection(handle, nil, db.Config{}); !errors.Is(err, db.ErrDatabaseUnavailable) {
		t.Fatalf("空 Builder 应返回 ErrDatabaseUnavailable，实际为 %v", err)
	}
	if err := handle.Ping(); err == nil {
		t.Fatal("依赖校验失败后 SQL 句柄必须已关闭")
	}
}

// TestConnectorValidationSupportsOptionalNeo4jDatabase 验证 Neo4j 默认数据库可省略，
// 而关系库与 MongoDB 仍必须显式给出数据库名。
func TestConnectorValidationSupportsOptionalNeo4jDatabase(t *testing.T) {
	config := db.Config{Type: "neo4j", Hostname: "localhost", Hostport: "7687"}
	if _, err := validateConnectorConfig(config, "neo4j", true, false); err != nil {
		t.Fatalf("Neo4j 应允许使用服务端默认数据库: %v", err)
	}
	config.Type = "mongo"
	if _, err := validateConnectorConfig(config, "mongo", true, true); !errors.Is(err, db.ErrInvalidDatabaseConfig) {
		t.Fatalf("MongoDB 缺少数据库名应返回 ErrInvalidDatabaseConfig，实际为 %v", err)
	}
}

// TestSQLPoolConfigurationRejectsOverflowAndInconsistentLimits 验证连接池参数不会溢出或自相矛盾。
func TestSQLPoolConfigurationRejectsOverflowAndInconsistentLimits(t *testing.T) {
	if _, err := resolveSQLConnectionPoolConfig(db.Config{MaxOpenConns: 1, MaxIdleConns: 2}); !errors.Is(err, db.ErrInvalidDatabaseConfig) {
		t.Fatalf("空闲连接超过最大连接应失败，实际为 %v", err)
	}
	if strconv.IntSize == 64 {
		overflowSeconds := int(math.MaxInt64/int64(time.Second)) + 1
		if _, err := resolveSQLConnectionPoolConfig(db.Config{ConnMaxLifetimeSeconds: overflowSeconds}); !errors.Is(err, db.ErrInvalidDatabaseConfig) {
			t.Fatalf("连接生命周期溢出应失败，实际为 %v", err)
		}
	}
}

// TestMysqlRejectsDangerousDriverFlags 验证高风险驱动开关不能通过 Params 绕过框架约束。
func TestMysqlRejectsDangerousDriverFlags(t *testing.T) {
	for _, key := range []string{"multiStatements", "allowAllFiles", "allowCleartextPasswords", "allowFallbackToPlaintext", "allowOldPasswords"} {
		config := db.Config{Username: "root", Hostname: "localhost", Hostport: "3306", Database: "app", Params: map[string]string{key: "true"}}
		if _, err := buildMysqlDSN(config); !errors.Is(err, db.ErrInvalidDatabaseConfig) {
			t.Fatalf("参数 %s=true 应被拒绝，实际为 %v", key, err)
		}
	}
}

// TestSqliteDSNEncodesParametersAndEnablesSafetyDefaults 验证 SQLite 参数不会通过字符串拼接串改。
func TestSqliteDSNEncodesParametersAndEnablesSafetyDefaults(t *testing.T) {
	dsn, err := buildSqliteDSN(db.Config{
		Database: "runtime/app data.db",
		Params:   map[string]string{"cache": "shared&mode=memory"},
	})
	if err != nil {
		t.Fatalf("构造 SQLite DSN 失败: %v", err)
	}
	parsed, err := url.Parse("file:" + dsn)
	if err != nil {
		t.Fatalf("解析 SQLite DSN 失败: %v", err)
	}
	query := parsed.Query()
	if query.Get("_foreign_keys") != "on" || query.Get("_busy_timeout") != "5000" || query.Get("_journal_mode") != "WAL" {
		t.Fatalf("SQLite 安全默认参数缺失: %v", query)
	}
	if query.Get("cache") != "shared&mode=memory" {
		t.Fatalf("SQLite 参数值未正确转义: %q", query.Get("cache"))
	}
	if _, err := buildSqliteDSN(db.Config{Database: "file:test.db?mode=memory"}); !errors.Is(err, db.ErrInvalidDatabaseConfig) {
		t.Fatalf("原始 URI 路径应被拒绝，实际为 %v", err)
	}
}

// TestPrepareSqliteFileCreatesPrivateRegularFile 验证 SQLite 不跟随符号链接且使用私有权限。
func TestPrepareSqliteFileCreatesPrivateRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	created, err := prepareSqliteFile(path)
	if err != nil || !created {
		t.Fatalf("创建 SQLite 文件失败: created=%v err=%v", created, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("读取 SQLite 文件失败: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("SQLite 文件权限应为 0600，实际为 %o", info.Mode().Perm())
	}

	target := filepath.Join(filepath.Dir(path), "target.db")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatalf("创建符号链接目标失败: %v", err)
	}
	link := filepath.Join(filepath.Dir(path), "link.db")
	if err := os.Symlink(target, link); err == nil {
		if _, err := prepareSqliteFile(link); !errors.Is(err, db.ErrInvalidDatabaseConfig) {
			t.Fatalf("SQLite 符号链接应被拒绝，实际为 %v", err)
		}
	}
}

// TestSqliteMemoryPoolRejectsMultipleConnections 验证内存库不会被连接池拆成多个独立数据库。
func TestSqliteMemoryPoolRejectsMultipleConnections(t *testing.T) {
	_, err := (&Sqlite{}).Connect(db.Config{Database: ":memory:", MaxOpenConns: 2})
	if !errors.Is(err, db.ErrInvalidDatabaseConfig) {
		t.Fatalf("多连接内存 SQLite 应返回 ErrInvalidDatabaseConfig，实际为 %v", err)
	}
}

func TestSqliteMemoryPoolDisablesExpiry(t *testing.T) {
	settings, err := sqlitePoolConfig(db.Config{
		Database:               ":memory:",
		ConnMaxLifetimeSeconds: 1,
		ConnMaxIdleTimeSeconds: 1,
	})
	if err != nil {
		t.Fatalf("resolve SQLite memory pool: %v", err)
	}
	if settings.MaxOpenConns != 1 || settings.MaxIdleConns != 1 || settings.ConnMaxLifetime != 0 || settings.ConnMaxIdleTime != 0 {
		t.Fatalf("SQLite memory pool must be 1/1/0/0: %#v", settings)
	}
}
