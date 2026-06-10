package db

import "testing"

// capturingConnection 记录最后一次写操作的入参，便于断言。
type capturingConnection struct {
	mockConnection
	lastTable string
	lastData  map[string]interface{}
	lastWhere []string
	lastArgs  []interface{}
}

func (c *capturingConnection) Update(table string, data map[string]interface{}, where []string, args []interface{}) (int64, error) {
	c.lastTable = table
	c.lastData = data
	c.lastWhere = where
	c.lastArgs = args
	return 1, nil
}

func (c *capturingConnection) Insert(table string, data map[string]interface{}) (int64, error) {
	c.lastTable = table
	c.lastData = data
	return 1, nil
}

type structUpdateUser struct {
	ID    int64  `thinkgo:"id"`
	Name  string `thinkgo:"name"`
	Email string `thinkgo:"email,omitempty"`
}

// TestModelStructUpdateUsesPrimaryKey 验证结构体更新以主键作为 WHERE 条件、
// 从 SET 中剔除主键，且 omitempty 字段为零值时被跳过。
func TestModelStructUpdateUsesPrimaryKey(t *testing.T) {
	conn := &capturingConnection{}
	database := NewDB(conn)
	model := NewModel(database, "user")

	err := model.Update(&structUpdateUser{ID: 7, Name: "alice"})
	if err != nil {
		t.Fatalf("结构体更新不应报错: %v", err)
	}

	if len(conn.lastWhere) != 1 || conn.lastWhere[0] != "id = ?" {
		t.Fatalf("应以主键作为 WHERE 条件，实际 where=%v", conn.lastWhere)
	}
	if len(conn.lastArgs) != 1 || conn.lastArgs[0] != int64(7) {
		t.Fatalf("WHERE 参数应为主键值 7，实际 args=%v", conn.lastArgs)
	}
	if _, ok := conn.lastData["id"]; ok {
		t.Fatalf("SET 子句不应包含主键，实际 data=%v", conn.lastData)
	}
	if _, ok := conn.lastData["email"]; ok {
		t.Fatalf("零值的 omitempty 字段应被跳过，实际 data=%v", conn.lastData)
	}
	if conn.lastData["name"] != "alice" {
		t.Fatalf("name 字段应被写入，实际 data=%v", conn.lastData)
	}
}

// TestModelStructUpdateRequiresPrimaryKey 验证缺少主键时拒绝更新，避免全表更新。
func TestModelStructUpdateRequiresPrimaryKey(t *testing.T) {
	conn := &capturingConnection{}
	database := NewDB(conn)
	model := NewModel(database, "user")

	if err := model.Update(&structUpdateUser{Name: "no-id"}); err == nil {
		t.Fatalf("零值主键应拒绝结构体更新")
	}
}
