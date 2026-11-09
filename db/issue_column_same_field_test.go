package db

import (
	"reflect"
	"testing"
)

// TestColumnSameFieldPortableAndGetter 验证非 SQL 投影及模型获取器保留原始字段类型。
func TestColumnSameFieldPortableAndGetter(t *testing.T) {
	connection := &modelBusinessConnection{rows: []map[string]interface{}{{"id": int64(7)}}}
	database := NewDB(connection)
	t.Cleanup(func() { _ = database.Close() })
	result, err := database.Table("items").Column("id", "id")
	if err != nil || !reflect.DeepEqual(result, map[string]interface{}{"7": int64(7)}) {
		t.Fatalf("可移植列查询失败: %v %v", result, err)
	}
	if connection.lastFieldsByTable["items"] != "id" {
		t.Fatalf("同一字段被重复投影: %q", connection.lastFieldsByTable["items"])
	}
	model := NewModel(database, "items")
	if err := model.Getter("id", func(value interface{}, row map[string]interface{}) interface{} {
		if row["id"] != value || value != int64(7) {
			t.Errorf("获取器收到字符串键覆盖的字段值: value=%v row=%v", value, row)
		}
		return value.(int64) + 1
	}); err != nil {
		t.Fatal(err)
	}
	result, err = model.newModelQuery().Column("id", "id")
	if err != nil || !reflect.DeepEqual(result, map[string]interface{}{"7": int64(8)}) {
		t.Fatalf("模型获取器应只转换值并保留原始键: %v %v", result, err)
	}
}
