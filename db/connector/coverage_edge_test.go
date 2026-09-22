package connector

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
)

func TestConnectorConnectEntryPointsRejectInvalidConfigurations(t *testing.T) {
	if _, err := (&Mysql{}).Connect(db.Config{
		Type: "mysql", Hostname: "localhost", Hostport: "3306", Database: "app",
		Params: map[string]string{"multiStatements": "true"},
	}); !errors.Is(err, db.ErrInvalidDatabaseConfig) {
		t.Fatalf("MySQL 危险参数应在 Connect 入口被拒绝: %v", err)
	}
	if _, err := (&Pgsql{}).Connect(db.Config{Type: "pgsql", Hostname: "bad host", Hostport: "5432", Database: "app"}); !errors.Is(err, db.ErrInvalidDatabaseConfig) {
		t.Fatalf("PostgreSQL 非法主机应在 Connect 入口被拒绝: %v", err)
	}
	if _, err := (&Sqlsrv{}).Connect(db.Config{Type: "sqlsrv", Hostname: "localhost", Hostport: "0", Database: "app"}); !errors.Is(err, db.ErrInvalidDatabaseConfig) {
		t.Fatalf("SQL Server 非法端口应在 Connect 入口被拒绝: %v", err)
	}
	if _, err := (&Mongo{}).Connect(db.Config{Type: "mongo", Hostname: "localhost", Hostport: "27017", Database: "bad/name"}); !errors.Is(err, db.ErrInvalidDatabaseConfig) {
		t.Fatalf("MongoDB 非法数据库名应在 Connect 入口被拒绝: %v", err)
	}
	if _, err := (&Neo4j{}).Connect(db.Config{Type: "neo4j", Hostname: "localhost", Hostport: "7687", Password: "secret"}); !errors.Is(err, db.ErrInvalidDatabaseConfig) {
		t.Fatalf("Neo4j 缺少用户名时应在 Connect 入口被拒绝: %v", err)
	}
	if _, err := (&Sqlite{}).Connect(db.Config{Type: "sqlite", Database: "runtime/app?mode=memory"}); !errors.Is(err, db.ErrInvalidDatabaseConfig) {
		t.Fatalf("SQLite URI 路径应在 Connect 入口被拒绝: %v", err)
	}
}

func TestConnectorHelperBranchesAndDatabaseHandleBoundaries(t *testing.T) {
	for _, name := range []string{"", "bad/name", "bad:name", "bad?name", "bad\\name", string(make([]byte, 64))} {
		if err := validateMongoDatabaseName(name); !errors.Is(err, db.ErrInvalidDatabaseConfig) {
			t.Fatalf("非法 MongoDB 数据库名 %q 应被拒绝: %v", name, err)
		}
	}
	if err := validateMongoDatabaseName("valid_database"); err != nil {
		t.Fatalf("合法 MongoDB 数据库名不应失败: %v", err)
	}
	if err := validateMysqlParameters(map[string]string{"multiStatements": "false"}); err != nil {
		t.Fatalf("false MySQL 参数不应失败: %v", err)
	}
	if err := validateMysqlParameters(nil); err != nil {
		t.Fatalf("空 MySQL 参数不应失败: %v", err)
	}

	if _, err := openSQLConnection("driver-that-does-not-exist", "", &builder.Sqlite{}, db.Config{}); err == nil {
		t.Fatal("未知 SQL 驱动应返回打开错误")
	}
	if _, err := verifySQLConnection(nil, &builder.Sqlite{}, db.Config{}); !errors.Is(err, db.ErrDatabaseUnavailable) {
		t.Fatalf("nil SQL 句柄应返回数据库不可用: %v", err)
	}
	if _, err := verifySQLConnection(nil, nil, db.Config{}); !errors.Is(err, db.ErrDatabaseUnavailable) {
		t.Fatalf("nil 句柄和 Builder 应返回数据库不可用: %v", err)
	}

	deferred := func() (recovered interface{}) {
		defer func() { recovered = recover() }()
		mustRegisterConnector("mysql", &Mysql{})
		return nil
	}()
	if deferred == nil {
		t.Fatal("重复注册内置 Connector 应触发保护性 panic")
	}
}

func TestPrepareSqliteFileHandlesExistingAndInvalidTargets(t *testing.T) {
	regular := filepath.Join(t.TempDir(), "existing.db")
	if err := os.WriteFile(regular, []byte("db"), 0o600); err != nil {
		t.Fatalf("创建已有 SQLite 文件失败: %v", err)
	}
	created, err := prepareSqliteFile(regular)
	if err != nil || created {
		t.Fatalf("已有 SQLite 文件不应重复创建: created=%t err=%v", created, err)
	}
	directory := filepath.Join(t.TempDir(), "directory.db")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatalf("创建 SQLite 目录失败: %v", err)
	}
	if _, err := prepareSqliteFile(directory); !errors.Is(err, db.ErrInvalidDatabaseConfig) {
		t.Fatalf("SQLite 目录路径应被拒绝: %v", err)
	}
	missingParent := filepath.Join(t.TempDir(), "missing", "app.db")
	if _, err := prepareSqliteFile(missingParent); err == nil {
		t.Fatal("SQLite 父目录不存在时应返回创建错误")
	}
}
