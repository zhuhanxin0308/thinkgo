package db

import (
	"context"
	"errors"
	"testing"
)

type relationMockConnection struct{ connectionIdentityState }

type relationContextConnection struct {
	modelBusinessConnection
	contexts []context.Context
}

func (c *relationContextConnection) Select(ctx context.Context, request SelectRequest) ([]map[string]interface{}, error) {
	c.contexts = append(c.contexts, ctx)
	return c.modelBusinessConnection.Select(ctx, request)
}

func (c *relationMockConnection) Select(_ context.Context, request SelectRequest) ([]map[string]interface{}, error) {
	switch request.Table() {
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

func (c *relationMockConnection) Insert(context.Context, InsertRequest) (InsertResult, error) {
	return InsertResult{}, nil
}

func (c *relationMockConnection) Update(context.Context, UpdateRequest) (UpdateResult, error) {
	return UpdateResult{}, nil
}

func (c *relationMockConnection) Delete(context.Context, DeleteRequest) (DeleteResult, error) {
	return DeleteResult{}, nil
}

func (c *relationMockConnection) Count(context.Context, CountRequest) (int64, error) {
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

	rows, err := userModel.With("profile", "posts", "company").SelectMaps()
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

// TestModelRelationPreloadingInheritsContext 验证主查询的请求上下文会传递到所有关联查询。
func TestModelRelationPreloadingInheritsContext(t *testing.T) {
	connection := &relationContextConnection{
		modelBusinessConnection: modelBusinessConnection{
			tableRows: map[string][]map[string]interface{}{
				"users":     {{"id": int64(1)}},
				"profiles":  {{"id": int64(2), "user_id": int64(1)}},
				"user_role": {{"user_id": int64(1), "role_id": int64(10)}},
				"roles":     {{"id": int64(10), "name": "admin"}},
			},
		},
	}
	database := NewDB(connection)
	users := NewModel(database, "users")
	if err := users.DefineHasOne("profile", NewModel(database, "profiles"), "user_id", "id"); err != nil {
		t.Fatal(err)
	}
	if err := users.DefineBelongsToMany("roles", NewModel(database, "roles"), "user_role", "user_id", "role_id", "id"); err != nil {
		t.Fatal(err)
	}

	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "request-trace")
	rows, err := users.With("profile", "roles").WithContext(ctx).SelectMaps()
	if err != nil {
		t.Fatalf("关联预加载不应失败: %v", err)
	}
	if len(rows) != 1 || len(connection.contexts) != 4 {
		t.Fatalf("关联查询数量不正确: rows=%#v contexts=%d", rows, len(connection.contexts))
	}
	if fields := connection.lastFieldsByTable["user_role"]; fields != "user_id, role_id" {
		t.Fatalf("多对多中间表应只投影关联键: fields=%q", fields)
	}
	for index, received := range connection.contexts {
		if received == nil || received.Value(contextKey{}) != "request-trace" {
			t.Fatalf("第 %d 条查询未继承请求上下文: %#v", index, received)
		}
	}
}

// TestModelQueryEachRejectsEagerRelations 验证流式模型查询不会偷偷退化为逐行关联查询。
func TestModelQueryEachRejectsEagerRelations(t *testing.T) {
	database := NewDB(&relationMockConnection{})
	users := NewModel(database, "users")
	if err := users.DefineHasOne("profile", NewModel(database, "profiles"), "user_id", "id"); err != nil {
		t.Fatal(err)
	}
	if err := users.With("profile").Each(func(map[string]interface{}) bool { return true }); !errors.Is(err, ErrUnsupportedFeature) {
		t.Fatalf("带关联的 Model Each 应明确拒绝: %v", err)
	}
}

func TestEagerRelationsGroupByRawKeysBeforeGetters(t *testing.T) {
	connection := &modelBusinessConnection{
		tableRows: map[string][]map[string]interface{}{
			"users": {
				{"id": int64(1), "manager_id": int64(10), "name": "Ada"},
			},
			"profiles": {
				{"id": int64(20), "user_id": int64(1), "label": "primary"},
			},
			"posts": {
				{"id": int64(30), "user_id": int64(1), "title": "first"},
				{"id": int64(31), "user_id": int64(1), "title": "second"},
			},
			"managers": {
				{"id": int64(10), "name": "manager"},
			},
			"user_role": {
				{"user_id": int64(1), "role_id": int64(100)},
				{"user_id": int64(1), "role_id": int64(101)},
			},
			"roles": {
				{"id": int64(100), "name": "admin"},
				{"id": int64(101), "name": "viewer"},
			},
		},
	}
	database := NewDB(connection)
	users := NewModel(database, "users")
	profiles := NewModel(database, "profiles")
	posts := NewModel(database, "posts")
	managers := NewModel(database, "managers")
	roles := NewModel(database, "roles")

	if err := profiles.Getter("user_id", func(interface{}, map[string]interface{}) interface{} { return "display-user" }); err != nil {
		t.Fatal(err)
	}
	if err := posts.Getter("user_id", func(interface{}, map[string]interface{}) interface{} { return "display-user" }); err != nil {
		t.Fatal(err)
	}
	if err := managers.Getter("id", func(interface{}, map[string]interface{}) interface{} { return "display-manager" }); err != nil {
		t.Fatal(err)
	}
	if err := roles.Getter("id", func(interface{}, map[string]interface{}) interface{} { return "display-role" }); err != nil {
		t.Fatal(err)
	}

	if err := users.DefineHasOne("profile", profiles, "user_id", "id"); err != nil {
		t.Fatal(err)
	}
	if err := users.DefineHasMany("posts", posts, "user_id", "id"); err != nil {
		t.Fatal(err)
	}
	if err := users.DefineBelongsTo("manager", managers, "manager_id", "id"); err != nil {
		t.Fatal(err)
	}
	if err := users.DefineBelongsToMany("roles", roles, "user_role", "user_id", "role_id", "id"); err != nil {
		t.Fatal(err)
	}

	rows, err := users.With("profile", "posts", "manager", "roles").SelectMaps()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected one user, got %#v", rows)
	}
	profile, profileOK := rows[0]["profile"].(map[string]interface{})
	manager, managerOK := rows[0]["manager"].(map[string]interface{})
	loadedPosts, postsOK := rows[0]["posts"].([]map[string]interface{})
	loadedRoles, rolesOK := rows[0]["roles"].([]map[string]interface{})
	if !profileOK || profile == nil || !managerOK || manager == nil || !postsOK || len(loadedPosts) != 2 || !rolesOK || len(loadedRoles) != 2 {
		t.Fatalf("raw-key eager loading failed: %#v", rows[0])
	}
	if profile["user_id"] != "display-user" || loadedPosts[0]["user_id"] != "display-user" || manager["id"] != "display-manager" || loadedRoles[0]["id"] != "display-role" {
		t.Fatalf("related getters were not applied after raw-key grouping: %#v", rows[0])
	}
}
