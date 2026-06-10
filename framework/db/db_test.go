package db

import "testing"

// TestResolveTableNameUsesPrefix 验证对外暴露的表名解析方法会自动拼接前缀。
func TestResolveTableNameUsesPrefix(t *testing.T) {
	db := NewDB(&mockConnection{})
	db.prefix = "tg_"

	fullName := db.ResolveTableName("graph_search_history")
	if fullName != "tg_graph_search_history" {
		t.Fatalf("解析后的完整表名错误，期望 tg_graph_search_history，实际为 %s", fullName)
	}
}

// TestResolveTableNameWithoutPrefix 验证无前缀时保持原始表名。
func TestResolveTableNameWithoutPrefix(t *testing.T) {
	db := NewDB(&mockConnection{})

	fullName := db.ResolveTableName("graph_search_history")
	if fullName != "graph_search_history" {
		t.Fatalf("无前缀时表名不应变化，期望 graph_search_history，实际为 %s", fullName)
	}
}
