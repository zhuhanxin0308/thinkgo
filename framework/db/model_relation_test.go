package db

import "testing"

type relationMockConnection struct{}

func (c *relationMockConnection) Select(table string, fields string, where []string, args []interface{}, order string, limit int, offset int) ([]map[string]interface{}, error) {
	switch table {
	case "users":
		return []map[string]interface{}{
			{"id": int64(1), "company_id": int64(10), "name": "张三"},
			{"id": int64(2), "company_id": int64(11), "name": "李四"},
		}, nil
	case "profiles":
		return []map[string]interface{}{
			{"id": int64(101), "user_id": int64(1), "bio": "张三简介"},
			{"id": int64(102), "user_id": int64(2), "bio": "李四简介"},
		}, nil
	case "posts":
		return []map[string]interface{}{
			{"id": int64(201), "user_id": int64(1), "title": "文章A"},
			{"id": int64(202), "user_id": int64(1), "title": "文章B"},
			{"id": int64(203), "user_id": int64(2), "title": "文章C"},
		}, nil
	case "companies":
		return []map[string]interface{}{
			{"id": int64(10), "name": "ThinkGo"},
			{"id": int64(11), "name": "ThinkPHP"},
		}, nil
	default:
		return nil, nil
	}
}

func (c *relationMockConnection) Insert(table string, data map[string]interface{}) (int64, error) {
	return 0, nil
}

func (c *relationMockConnection) Update(table string, data map[string]interface{}, where []string, args []interface{}) (int64, error) {
	return 0, nil
}

func (c *relationMockConnection) Delete(table string, where []string, args []interface{}) (int64, error) {
	return 0, nil
}

func (c *relationMockConnection) Count(table string, where []string, args []interface{}) (int64, error) {
	return 0, nil
}

func (c *relationMockConnection) Close() error {
	return nil
}

func TestModelWithRelations(t *testing.T) {
	database := NewDB(&relationMockConnection{})
	userModel := NewModel(database, "users")
	userModel.DefineHasOne("profile", NewModel(database, "profiles"), "user_id", "id")
	userModel.DefineHasMany("posts", NewModel(database, "posts"), "user_id", "id")
	userModel.DefineBelongsTo("company", NewModel(database, "companies"), "company_id", "id")

	rows, err := userModel.With("profile", "posts", "company").Select()
	if err != nil {
		t.Fatalf("关联预加载不应报错，实际为 %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("用户列表长度不正确，实际为 %d", len(rows))
	}

	profile, ok := rows[0]["profile"].(map[string]interface{})
	if !ok || profile["bio"] != "张三简介" {
		t.Fatalf("HasOne 关联装载不正确，实际为 %#v", rows[0]["profile"])
	}
	posts, ok := rows[0]["posts"].([]map[string]interface{})
	if !ok || len(posts) != 2 {
		t.Fatalf("HasMany 关联装载不正确，实际为 %#v", rows[0]["posts"])
	}
	company, ok := rows[1]["company"].(map[string]interface{})
	if !ok || company["name"] != "ThinkPHP" {
		t.Fatalf("BelongsTo 关联装载不正确，实际为 %#v", rows[1]["company"])
	}
}
