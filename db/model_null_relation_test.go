package db

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func nullRelationFixture(t *testing.T, mixed bool) (*Model, *relationContextConnection) {
	t.Helper()
	rows := []map[string]interface{}{{"id": int64(1), "relation_id": nil, "company_id": nil}}
	if mixed {
		rows = append(rows, map[string]interface{}{"id": int64(2), "relation_id": int64(2), "company_id": int64(10)})
	}
	connection := &relationContextConnection{modelBusinessConnection: modelBusinessConnection{tableRows: map[string][]map[string]interface{}{
		"users":     rows,
		"profiles":  {{"id": int64(20), "user_id": int64(2)}},
		"posts":     {{"id": int64(30), "user_id": int64(2)}},
		"companies": {{"id": int64(10)}},
		"user_role": {{"user_id": int64(2), "role_id": int64(40)}},
		"roles":     {{"id": int64(40)}},
	}}}
	database := NewDB(connection)
	users := NewModel(database, "users")
	for _, err := range []error{
		users.DefineHasOne("profile", NewModel(database, "profiles"), "user_id", "relation_id"),
		users.DefineHasMany("posts", NewModel(database, "posts"), "user_id", "relation_id"),
		users.DefineBelongsTo("company", NewModel(database, "companies"), "company_id", "id"),
		users.DefineBelongsToMany("roles", NewModel(database, "roles"), "user_role", "user_id", "role_id", "relation_id"),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	return users, connection
}

// TestEmptyRelationDoesNotMaskMissingKey 区分合法 NULL 与查询未返回关联键，避免吞掉投影错误。
func TestEmptyRelationDoesNotMaskMissingKey(t *testing.T) {
	for _, relation := range []string{"profile", "posts", "company", "roles"} {
		users, connection := nullRelationFixture(t, false)
		key := "relation_id"
		if relation == "company" {
			key = "company_id"
		}
		delete(connection.tableRows["users"][0], key)
		if _, err := users.With(relation).SelectMaps(); !errors.Is(err, ErrInvalidRelation) {
			t.Fatalf("关联 %q 的缺失键未被拒绝: %v", relation, err)
		}
		if len(connection.contexts) != 1 {
			t.Fatal("缺失关联键时不应执行关联查询")
		}
	}
}

// TestNullRelationShapeDoesNotDependOnBatch 验证四种关联在全 NULL、混合批次和单条查询中形状一致。
func TestNullRelationShapeDoesNotDependOnBatch(t *testing.T) {
	for _, relations := range [][]string{{"profile"}, {"posts"}, {"company"}, {"roles"}, {"profile", "posts", "company", "roles"}} {
		for _, mixed := range []bool{false, true} {
			for _, single := range []bool{false, true} {
				t.Run(fmt.Sprintf("%v/混合=%t/单条=%t", relations, mixed, single), func(t *testing.T) {
					users, connection := nullRelationFixture(t, mixed)
					query := users.With(relations...)
					var row map[string]interface{}
					if single {
						var err error
						row, err = query.FindMap()
						if err != nil {
							t.Fatal(err)
						}
					} else {
						rows, err := query.SelectMaps()
						if err != nil || len(rows) == 0 {
							t.Fatal("查询没有返回源行", err)
						}
						row = rows[0]
					}
					for _, relation := range relations {
						value, exists := row[relation]
						if !exists {
							t.Fatalf("请求的关联字段 %q 被省略", relation)
						}
						want := "null"
						if relation == "posts" || relation == "roles" {
							want = "[]"
							if _, ok := value.([]map[string]interface{}); !ok {
								t.Fatalf("集合关联类型不一致: %T", value)
							}
						} else if _, ok := value.(map[string]interface{}); !ok {
							t.Fatalf("单对象关联类型不一致: %T", value)
						}
						encoded, err := json.Marshal(value)
						if err != nil || string(encoded) != want {
							t.Fatalf("关联 %q JSON 形状=%s，预期=%s，错误=%v", relation, encoded, want, err)
						}
					}
					if (!mixed || single) && len(connection.contexts) != 1 {
						t.Fatalf("全空关联键执行了多余查询: %d", len(connection.contexts))
					}
				})
			}
		}
	}
}
