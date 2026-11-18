package framework

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
)

type batch5SQLConnector struct {
	pingErr error
	closed  *atomic.Int32
}

func (c batch5SQLConnector) Open(string) (driver.Conn, error) {
	return batch5SQLConn(c), nil
}

func (c batch5SQLConnector) Connect(context.Context) (driver.Conn, error) {
	if c.pingErr != nil {
		return nil, c.pingErr
	}
	return batch5SQLConn(c), nil
}

func (c batch5SQLConnector) Driver() driver.Driver { return c }

type batch5SQLConn struct {
	pingErr error
	closed  *atomic.Int32
}

func (c batch5SQLConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unsupported") }
func (c batch5SQLConn) Begin() (driver.Tx, error)           { return nil, errors.New("unsupported") }
func (c batch5SQLConn) Close() error {
	if c.closed != nil {
		c.closed.Add(1)
	}
	return nil
}
func (c batch5SQLConn) Ping(context.Context) error { return c.pingErr }

func newBatch5StubDatabase(t *testing.T, pingErr error) (*db.DB, *atomic.Int32) {
	t.Helper()
	closed := &atomic.Int32{}
	raw := sql.OpenDB(batch5SQLConnector{pingErr: pingErr, closed: closed})
	return db.NewDB(db.NewSQLConnection(raw, &builder.Sqlite{})), closed
}

func writeBatch5BaseConfigs(t *testing.T, basePath, appJSON string) {
	t.Helper()
	configDir := filepath.Join(basePath, "config")
	if err := os.MkdirAll(configDir, 0o750); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"app.json":    appJSON,
		"cache.json":  `{"default":"memory","stores":{"memory":{"type":"memory"}}}`,
		"log.json":    `{"close":true,"default":"file","channels":{"file":{"type":"file","path":"runtime/log"}}}`,
		"cookie.json": `{"secret":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(configDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAppBatch5RuntimePathsSkipCacheAndUseDatabase(t *testing.T) {
	var nilApp *App
	if nilApp.ProjectRuntimePath() != "" || nilApp.NativeApplicationPath("index") != "" {
		t.Fatal("nil App 路径辅助方法应返回空串")
	}
	nilApp.SkipConfigurationCache()
	if err := nilApp.UseDatabase(nil); !errors.Is(err, ErrNilApplication) {
		t.Fatalf("nil App UseDatabase 应返回 ErrNilApplication: %v", err)
	}

	basePath := t.TempDir()
	for _, sub := range []string{"index", "admin"} {
		if err := os.MkdirAll(filepath.Join(basePath, "app", sub), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	writeBatch5BaseConfigs(t, basePath, `{"app_name":"from-source","app_env":"test"}`)
	for _, broken := range []string{
		filepath.Join(basePath, "runtime", "config.json"),
		filepath.Join(basePath, "runtime", "index", "config.json"),
		filepath.Join(basePath, "runtime", "admin", "config.json"),
	} {
		if err := os.MkdirAll(filepath.Dir(broken), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(broken, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	app := NewConsoleAppUninitialized(basePath)
	t.Cleanup(func() { _ = app.Close() })
	noop := func(*App) error { return nil }
	if err := app.RegisterApplications(noop,
		ApplicationDefinition{Name: "index", Register: noop},
		ApplicationDefinition{Name: "admin", Register: noop},
	); err != nil {
		t.Fatal(err)
	}
	app.SkipConfigurationCache()
	if err := app.Initialize(); err != nil {
		t.Fatalf("SkipConfigurationCache 后损坏的根/子缓存不应阻断启动: %v", err)
	}
	if got, want := app.ProjectRuntimePath(), filepath.Join(basePath, "runtime"); got != want {
		t.Fatalf("ProjectRuntimePath 错误: got=%q want=%q", got, want)
	}
	if got, want := app.NativeApplicationPath("admin"), filepath.Join(basePath, "app", "admin"); got != want {
		t.Fatalf("NativeApplicationPath(admin) 错误: got=%q want=%q", got, want)
	}
	if got, want := app.NativeApplicationPath("index"), filepath.Join(basePath, "app", "index"); got != want {
		t.Fatalf("NativeApplicationPath(index) 错误: got=%q want=%q", got, want)
	}
	if got, want := app.NativeApplicationPath(""), filepath.Join(basePath, "app", "index"); got != want {
		t.Fatalf("NativeApplicationPath(\"\") 错误: got=%q want=%q", got, want)
	}
	children, err := app.BuildApplications()
	if err != nil {
		t.Fatal(err)
	}
	if err := children["admin"].Initialize(); err != nil {
		t.Fatalf("子应用应继承 SkipConfigurationCache 忽略损坏缓存: %v", err)
	}

	// 验证 UseDatabase 的空连接、重复注入、与 database.connections 冲突及 required 策略探针失败。
	dbBase := t.TempDir()
	writeBatch5BaseConfigs(t, dbBase, `{"app_env":"test","database_startup_policy":"required"}`)
	withBadPing := NewApp(dbBase)
	if err := withBadPing.UseDatabase(nil); !errors.Is(err, db.ErrDatabaseUnavailable) {
		t.Fatalf("nil 数据库应返回 ErrDatabaseUnavailable: %v", err)
	}
	failingDB, _ := newBatch5StubDatabase(t, errors.New("ping down"))
	if err := withBadPing.UseDatabase(failingDB); err != nil {
		t.Fatalf("首次 UseDatabase 应成功: %v", err)
	}
	extraDB, extraClosed := newBatch5StubDatabase(t, nil)
	if err := withBadPing.UseDatabase(extraDB); !errors.Is(err, ErrDuplicateRegistration) {
		t.Fatalf("重复 UseDatabase 应被拒绝: %v", err)
	}
	_ = extraDB.Close()
	_ = extraClosed
	if err := withBadPing.Initialize(); err == nil {
		t.Fatal("required 策略下显式注入不可达数据库应阻止启动")
	}
	_ = withBadPing.Close()

	conflictBase := t.TempDir()
	writeBatch5BaseConfigs(t, conflictBase, `{"app_env":"test"}`)
	if err := os.WriteFile(filepath.Join(conflictBase, "config", "database.json"), []byte(`{"default":"sqlite","connections":{"sqlite":{"type":"sqlite","database":":memory:"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	conflictApp := NewApp(conflictBase)
	conflictDB, _ := newBatch5StubDatabase(t, nil)
	if err := conflictApp.UseDatabase(conflictDB); err != nil {
		t.Fatal(err)
	}
	if err := conflictApp.Initialize(); !errors.Is(err, db.ErrInvalidDatabaseConfig) {
		t.Fatalf("UseDatabase 与 database.connections 并存应报配置冲突: %v", err)
	}
	_ = conflictApp.Close()
}
