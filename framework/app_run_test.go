package framework

import (
	"errors"
	"testing"

	"thinkgo/framework/db"
	"thinkgo/framework/log"
)

type appRunLogDriver struct {
	closed  bool
	entries []*log.LogEntry
}

func (d *appRunLogDriver) SaveEntries(entries []*log.LogEntry) error {
	d.entries = append(d.entries, entries...)
	return nil
}

func (d *appRunLogDriver) WriteEntry(entry *log.LogEntry) error {
	d.entries = append(d.entries, entry)
	return nil
}

func (d *appRunLogDriver) Close() error {
	d.closed = true
	return nil
}

type appRunConnection struct {
	closed bool
}

func (c *appRunConnection) Select(table string, fields string, where []string, args []interface{}, order string, limit int, offset int) ([]map[string]interface{}, error) {
	return nil, nil
}

func (c *appRunConnection) Insert(table string, data map[string]interface{}) (int64, error) {
	return 0, nil
}

func (c *appRunConnection) Update(table string, data map[string]interface{}, where []string, args []interface{}) (int64, error) {
	return 0, nil
}

func (c *appRunConnection) Delete(table string, where []string, args []interface{}) (int64, error) {
	return 0, nil
}

func (c *appRunConnection) Count(table string, where []string, args []interface{}) (int64, error) {
	return 0, nil
}

func (c *appRunConnection) Close() error {
	c.closed = true
	return nil
}

type appRunKernel struct {
	err error
}

func (k *appRunKernel) Run() error {
	return k.err
}

// TestAppRunShutsDownLogAndDatabase 验证应用退出时会关闭日志与数据库资源。
func TestAppRunShutsDownLogAndDatabase(t *testing.T) {
	driver := &appRunLogDriver{}
	conn := &appRunConnection{}
	app := &App{
		Log:    log.NewLog(driver),
		DB:     db.NewDB(conn),
		Kernel: &appRunKernel{},
	}

	app.Run()

	if !driver.closed {
		t.Fatal("App.Run 结束后应关闭日志驱动")
	}
	if !conn.closed {
		t.Fatal("App.Run 结束后应关闭数据库连接")
	}
}

// TestAppRunPanicsWhenStartupErrorExists 验证存在启动致命错误时不会继续进入内核运行。
func TestAppRunPanicsWhenStartupErrorExists(t *testing.T) {
	driver := &appRunLogDriver{}
	app := &App{
		Log:        log.NewLog(driver),
		startupErr: errors.New("startup failed"),
	}

	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("存在 startupErr 时 Run 应终止启动流程")
		}
		if !driver.closed {
			t.Fatal("启动失败时也应关闭日志驱动")
		}
	}()

	app.Run()
}
