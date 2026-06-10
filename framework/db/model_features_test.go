package db

import (
	"strings"
	"testing"
)

// ==================== 多对多关联测试 ====================

// TestDefineBelongsToMany 验证多对多关联定义。
func TestDefineBelongsToMany(t *testing.T) {
	conn := &mockConnection{}
	database := NewDB(conn)

	userModel := NewModel(database, "users")
	roleModel := NewModel(database, "roles")

	userModel.DefineBelongsToMany("roles", roleModel, "user_role", "user_id", "role_id", "id")

	rel, ok := userModel.relations["roles"]
	if !ok {
		t.Fatal("多对多关联 roles 未注册")
	}
	if rel.Type != relationBelongsToMany {
		t.Fatalf("关联类型不正确，期望 belongsToMany，实际 %s", rel.Type)
	}
	if rel.PivotTable != "user_role" {
		t.Fatalf("中间表名不正确，期望 user_role，实际 %s", rel.PivotTable)
	}
	if rel.ForeignKey != "user_id" {
		t.Fatalf("外键不正确，期望 user_id，实际 %s", rel.ForeignKey)
	}
	if rel.RelatedForeignKey != "role_id" {
		t.Fatalf("关联外键不正确，期望 role_id，实际 %s", rel.RelatedForeignKey)
	}
}

// ==================== 获取器测试 ====================

// TestModelGetterTransformsField 验证获取器在 Find 时自动转换字段值。
func TestModelGetterTransformsField(t *testing.T) {
	conn := &mockConnection{}
	database := NewDB(conn)

	model := NewModel(database, "users")
	model.Getter("table", func(value interface{}, data map[string]interface{}) interface{} {
		if s, ok := value.(string); ok {
			return strings.ToUpper(s)
		}
		return value
	})

	row, err := model.Find()
	if err != nil {
		t.Fatalf("Find 不应返回错误，实际为 %v", err)
	}
	if row == nil {
		t.Fatal("Find 返回了 nil")
	}
	// mockConnection 的 Select 返回 table 字段
	if tableVal, ok := row["table"].(string); ok {
		if tableVal != strings.ToUpper(tableVal) {
			t.Fatalf("获取器应将 table 字段转为大写，实际为 %q", tableVal)
		}
	}
}

// TestModelGetterOnSelect 验证获取器在 Select 时对每行都生效。
func TestModelGetterOnSelect(t *testing.T) {
	conn := &mockConnection{}
	database := NewDB(conn)

	model := NewModel(database, "users")
	transformCalled := 0
	model.Getter("table", func(value interface{}, data map[string]interface{}) interface{} {
		transformCalled++
		return value
	})

	rows, err := model.Select()
	if err != nil {
		t.Fatalf("Select 不应返回错误，实际为 %v", err)
	}
	if transformCalled != len(rows) {
		t.Fatalf("获取器应对每行调用，期望 %d 次，实际 %d 次", len(rows), transformCalled)
	}
}

// ==================== 修改器测试 ====================

// TestModelSetterTransformsOnInsert 验证修改器在 Insert 时自动转换字段值。
func TestModelSetterTransformsOnInsert(t *testing.T) {
	conn := &timestampCaptureConnection{}
	database := NewDB(conn)

	model := NewModel(database, "users")
	model.Setter("username", func(value interface{}, data map[string]interface{}) interface{} {
		if s, ok := value.(string); ok {
			return strings.ToLower(s)
		}
		return value
	})

	_, err := model.Insert(map[string]interface{}{"username": "ADMIN"})
	if err != nil {
		t.Fatalf("Insert 不应返回错误，实际为 %v", err)
	}
	if conn.insertData["username"] != "admin" {
		t.Fatalf("修改器应将 username 转为小写，实际为 %v", conn.insertData["username"])
	}
}

// TestModelSetterTransformsOnUpdate 验证修改器在 UpdateMap 时自动转换。
func TestModelSetterTransformsOnUpdate(t *testing.T) {
	conn := &timestampCaptureConnection{}
	database := NewDB(conn)

	model := NewModel(database, "users")
	model.Setter("email", func(value interface{}, data map[string]interface{}) interface{} {
		if s, ok := value.(string); ok {
			return strings.TrimSpace(s)
		}
		return value
	})

	// 通过 Where 获取 Query 并走 Query.Update，但修改器应在 Model 层面生效
	// 这里直接测试 applySetters 内部机制
	data := map[string]interface{}{"email": "  test@test.com  "}
	model.applySetters(data)
	if data["email"] != "test@test.com" {
		t.Fatalf("修改器应去除首尾空格，实际为 %q", data["email"])
	}
}

// ==================== 搜索器测试 ====================

// TestModelSearcher 验证搜索器能正确应用查询条件。
func TestModelSearcher(t *testing.T) {
	conn := &mockConnection{}
	database := NewDB(conn)

	model := NewModel(database, "users")
	model.Searcher("name", func(q *Query, value interface{}, data map[string]interface{}) {
		q.Where("name LIKE ?", "%"+value.(string)+"%")
	})
	model.Searcher("status", func(q *Query, value interface{}, data map[string]interface{}) {
		q.Where("status = ?", value)
	})

	q := model.WithSearch(
		[]string{"name", "status"},
		map[string]interface{}{
			"name":   "admin",
			"status": 1,
		},
	)

	finalQ := q.PrepareQuery()
	if len(finalQ.where) != 2 {
		t.Fatalf("搜索器应生成 2 个条件，实际 %d 个: %v", len(finalQ.where), finalQ.where)
	}
}

// TestModelSearcherIgnoresUnregistered 验证未注册的搜索器字段被忽略。
func TestModelSearcherIgnoresUnregistered(t *testing.T) {
	conn := &mockConnection{}
	database := NewDB(conn)

	model := NewModel(database, "users")
	model.Searcher("name", func(q *Query, value interface{}, data map[string]interface{}) {
		q.Where("name = ?", value)
	})

	q := model.WithSearch(
		[]string{"name", "nonexistent"},
		map[string]interface{}{
			"name":        "admin",
			"nonexistent": "value",
		},
	)

	finalQ := q.PrepareQuery()
	if len(finalQ.where) != 1 {
		t.Fatalf("未注册的搜索器应被忽略，期望 1 个条件，实际 %d 个", len(finalQ.where))
	}
}

// TestModelSearcherIgnoresMissingData 验证数据中缺失的字段不触发搜索器。
func TestModelSearcherIgnoresMissingData(t *testing.T) {
	conn := &mockConnection{}
	database := NewDB(conn)

	model := NewModel(database, "users")
	model.Searcher("name", func(q *Query, value interface{}, data map[string]interface{}) {
		q.Where("name = ?", value)
	})

	q := model.WithSearch(
		[]string{"name"},
		map[string]interface{}{}, // 数据中没有 name
	)

	finalQ := q.PrepareQuery()
	if len(finalQ.where) != 0 {
		t.Fatalf("数据缺失时搜索器不应触发，实际生成了 %d 个条件", len(finalQ.where))
	}
}

// TestModelWithoutGettersSetter 验证没有注册获取器/修改器时 CRUD 正常。
func TestModelWithoutGettersSetter(t *testing.T) {
	conn := &mockConnection{}
	database := NewDB(conn)

	model := NewModel(database, "users")
	_, err := model.Find()
	if err != nil {
		t.Fatalf("无获取器时 Find 不应失败，实际为 %v", err)
	}

	_, err = model.Insert(map[string]interface{}{"name": "test"})
	if err != nil {
		t.Fatalf("无修改器时 Insert 不应失败，实际为 %v", err)
	}
}
