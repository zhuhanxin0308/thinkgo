package db

import (
	"testing"
)

type fakeManagerConnection struct {
	name string
}

func (c *fakeManagerConnection) Select(table string, fields string, where []string, args []interface{}, order string, limit int, offset int) ([]map[string]interface{}, error) {
	return nil, nil
}

func (c *fakeManagerConnection) Insert(table string, data map[string]interface{}) (int64, error) {
	return 0, nil
}

func (c *fakeManagerConnection) Update(table string, data map[string]interface{}, where []string, args []interface{}) (int64, error) {
	return 0, nil
}

func (c *fakeManagerConnection) Delete(table string, where []string, args []interface{}) (int64, error) {
	return 0, nil
}

func (c *fakeManagerConnection) Count(table string, where []string, args []interface{}) (int64, error) {
	return 0, nil
}

func (c *fakeManagerConnection) Close() error {
	return nil
}

func TestManagerReturnsNamedConnections(t *testing.T) {
	manager := NewManager("primary")
	primary := NewDB(&fakeManagerConnection{name: "primary"})
	analytics := NewDB(&fakeManagerConnection{name: "analytics"})

	if err := manager.Add("primary", primary); err != nil {
		t.Fatalf("注册默认连接失败，错误为 %v", err)
	}
	if err := manager.Add("analytics", analytics); err != nil {
		t.Fatalf("注册命名连接失败，错误为 %v", err)
	}

	defaultConnection, err := manager.Default()
	if err != nil {
		t.Fatalf("读取默认连接失败，错误为 %v", err)
	}
	if defaultConnection != primary {
		t.Fatal("默认连接返回不正确")
	}
	conn, err := manager.Connection("analytics")
	if err != nil {
		t.Fatalf("命名连接读取失败，错误为 %v", err)
	}
	if conn != analytics {
		t.Fatal("命名连接实例不正确")
	}
}
