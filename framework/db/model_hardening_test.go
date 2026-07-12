package db

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"thinkgo/framework/db/builder"
)

type modelHardeningUser struct {
	UserID uint64 `thinkgo:"user_id"`
	Name   string `thinkgo:"name"`
}

// TestModelTypeInferenceRejectsInvalidValues 验证表名推断只接受结构体类型。
func TestModelTypeInferenceRejectsInvalidValues(t *testing.T) {
	if _, err := GetTableName(nil); !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("nil 模型应返回 ErrInvalidModel，实际为 %v", err)
	}
	if _, err := GetTableName(42); !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("非结构体模型应返回 ErrInvalidModel，实际为 %v", err)
	}
	name, err := GetTableName((*modelHardeningUser)(nil))
	if err != nil || name != "model_hardening_user" {
		t.Fatalf("类型化 nil 指针应可推断类型，name=%q err=%v", name, err)
	}
}

// TestInvalidModelDependenciesReturnErrors 验证无效模型不会在查询创建阶段崩溃。
func TestInvalidModelDependenciesReturnErrors(t *testing.T) {
	if _, err := NewModel(nil, "users").Select(); !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("nil DB 模型应返回 ErrInvalidModel，实际为 %v", err)
	}
	if _, err := NewModel(NewDB(&mockConnection{}), "").Select(); !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("空表名模型应返回 ErrInvalidModel，实际为 %v", err)
	}
}

// TestModelCreateRequiresPointerAndFillsConfiguredPrimaryKey 验证 Create 可按标签回填不同整数类型主键。
func TestModelCreateRequiresPointerAndFillsConfiguredPrimaryKey(t *testing.T) {
	database := NewDB(&queryHardeningConnection{})
	model := NewModel(database, "users").PrimaryKey("user_id")
	if err := model.Create(modelHardeningUser{Name: "Ada"}); !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("非指针 Create 应返回 ErrInvalidModel，实际为 %v", err)
	}
	var nilUser *modelHardeningUser
	if err := model.Create(nilUser); !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("类型化 nil Create 应返回 ErrInvalidModel，实际为 %v", err)
	}

	user := &modelHardeningUser{Name: "Ada"}
	if err := model.Create(user); err != nil {
		t.Fatalf("创建结构体失败: %v", err)
	}
	if user.UserID != 1 {
		t.Fatalf("自定义 uint64 主键未回填，实际为 %d", user.UserID)
	}
}

// TestModelPassesConfiguredPrimaryKeyToReturningDialect 验证 PostgreSQL 类方言不会硬编码 RETURNING id。
func TestModelPassesConfiguredPrimaryKeyToReturningDialect(t *testing.T) {
	connection, recorder := newRecordingHardeningSQLConnection(t)
	connection.Builder = &builder.Pgsql{}
	model := NewModel(NewDB(connection), "users").PrimaryKey("user_id")
	data := map[string]interface{}{"name": "Ada"}
	if _, err := model.Insert(data); err != nil {
		t.Fatalf("RETURNING 方言插入失败: %v", err)
	}
	if !strings.Contains(recorder.recordedQuery(), `RETURNING "user_id"`) {
		t.Fatalf("未使用模型主键构造 RETURNING: %q", recorder.recordedQuery())
	}
}

// TestModelMapWritesDoNotMutateCallerData 验证修改器、事件和主键回填只作用于工作副本。
func TestModelMapWritesDoNotMutateCallerData(t *testing.T) {
	database := NewDB(&queryHardeningConnection{})
	model := NewModel(database, "users")
	if err := model.Setter("name", func(value interface{}, _ map[string]interface{}) interface{} {
		return strings.ToLower(value.(string))
	}); err != nil {
		t.Fatalf("注册修改器失败: %v", err)
	}
	if err := model.On(ModelBeforeInsert, func(data map[string]interface{}) bool {
		data["internal_marker"] = true
		return true
	}); err != nil {
		t.Fatalf("注册事件失败: %v", err)
	}

	data := map[string]interface{}{"name": "ADA"}
	before := cloneHardeningMap(data)
	if _, err := model.Insert(data); err != nil {
		t.Fatalf("模型插入失败: %v", err)
	}
	if !reflect.DeepEqual(data, before) {
		t.Fatalf("模型写入不得修改调用方 map，实际为 %#v", data)
	}
}

// TestModelRegistrationRejectsNilAndInvalidDefinitions 验证配置错误在注册阶段显式返回。
func TestModelRegistrationRejectsNilAndInvalidDefinitions(t *testing.T) {
	model := NewModel(NewDB(&mockConnection{}), "users")
	if err := model.Getter("name", nil); !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("nil 获取器应返回 ErrInvalidModel，实际为 %v", err)
	}
	if err := model.Setter("name", nil); !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("nil 修改器应返回 ErrInvalidModel，实际为 %v", err)
	}
	if err := model.Searcher("name", nil); !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("nil 搜索器应返回 ErrInvalidModel，实际为 %v", err)
	}
	if err := model.On(ModelBeforeInsert, nil); !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("nil 事件应返回 ErrInvalidModel，实际为 %v", err)
	}
	getter := func(value interface{}, _ map[string]interface{}) interface{} { return value }
	if err := model.Getter("name", getter); err != nil {
		t.Fatalf("注册合法获取器失败: %v", err)
	}
	if err := model.Getter("name", getter); !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("重复获取器应返回 ErrInvalidModel，实际为 %v", err)
	}
	if err := model.DefineHasOne("profile", nil, "user_id", "id"); !errors.Is(err, ErrInvalidRelation) {
		t.Fatalf("nil 关联模型应返回 ErrInvalidRelation，实际为 %v", err)
	}
	related := NewModel(NewDB(&mockConnection{}), "profiles")
	if err := model.DefineHasOne("profile", related, "bad key", "id"); !errors.Is(err, ErrInvalidRelation) {
		t.Fatalf("非法关联字段应返回 ErrInvalidRelation，实际为 %v", err)
	}
	if err := model.DefineHasOne("profile", related, "user_id", "id"); err != nil {
		t.Fatalf("注册合法关联失败: %v", err)
	}
	if err := model.DefineHasOne("profile", related, "user_id", "id"); !errors.Is(err, ErrInvalidRelation) {
		t.Fatalf("重复关联名应返回 ErrInvalidRelation，实际为 %v", err)
	}
}

// TestSoftDeleteRequiresBusinessPredicate 验证框架自动注入的软删除条件不能绕过全表写保护。
func TestSoftDeleteRequiresBusinessPredicate(t *testing.T) {
	connection := &queryHardeningConnection{}
	model := NewModel(NewDB(connection), "users").SoftDelete()
	if _, err := model.newModelQuery().Update(map[string]interface{}{"name": "all"}); !errors.Is(err, ErrUnsafeFullTableMutation) {
		t.Fatalf("软删除条件不得让无业务条件更新通过，实际为 %v", err)
	}
	if err := model.Delete(); !errors.Is(err, ErrUnsafeFullTableMutation) {
		t.Fatalf("无业务条件软删除应返回 ErrUnsafeFullTableMutation，实际为 %v", err)
	}
	if err := model.Restore(); !errors.Is(err, ErrUnsafeFullTableMutation) {
		t.Fatalf("无业务条件恢复应返回 ErrUnsafeFullTableMutation，实际为 %v", err)
	}
	if _, err := model.Where("id = ?", 1).Delete(); err != nil {
		t.Fatalf("带业务条件软删除失败: %v", err)
	}
}

// TestRestoreDoesNotMutateReusableModelQuery 验证重复执行 Restore 不会累加内部条件。
func TestRestoreDoesNotMutateReusableModelQuery(t *testing.T) {
	model := NewModel(NewDB(&queryHardeningConnection{}), "users").SoftDelete()
	query := model.Where("id = ?", 1)
	if _, err := query.Restore(); err != nil {
		t.Fatalf("首次恢复失败: %v", err)
	}
	if _, err := query.Restore(); err != nil {
		t.Fatalf("重复恢复失败: %v", err)
	}
	if len(query.query.where) != 1 {
		t.Fatalf("Restore 不得污染可复用查询，实际条件为 %v", query.query.where)
	}
}

type relationHardeningConnection struct {
	missingParentKey bool
}

func (c *relationHardeningConnection) Select(table, _ string, _ []string, _ []interface{}, _ string, _ int, _ int) ([]map[string]interface{}, error) {
	switch table {
	case "users":
		if c.missingParentKey {
			return []map[string]interface{}{{"name": "missing"}}, nil
		}
		return []map[string]interface{}{{"id": int64(1)}, {"id": "1"}}, nil
	case "profiles":
		return []map[string]interface{}{
			{"user_id": int64(1), "label": "numeric", "status": 1},
			{"user_id": "1", "label": "string", "status": 1},
		}, nil
	default:
		return nil, nil
	}
}

func (c *relationHardeningConnection) Insert(string, map[string]interface{}) (int64, error) {
	return 1, nil
}
func (c *relationHardeningConnection) Update(string, map[string]interface{}, []string, []interface{}) (int64, error) {
	return 1, nil
}
func (c *relationHardeningConnection) Delete(string, []string, []interface{}) (int64, error) {
	return 1, nil
}
func (c *relationHardeningConnection) Count(string, []string, []interface{}) (int64, error) {
	return 0, nil
}
func (c *relationHardeningConnection) Close() error { return nil }

// TestRelationLoadingKeepsKeyTypesAndAppliesRelatedGetters 验证 int(1) 与 string("1") 不会串组。
func TestRelationLoadingKeepsKeyTypesAndAppliesRelatedGetters(t *testing.T) {
	connection := &relationHardeningConnection{}
	database := NewDB(connection)
	users := NewModel(database, "users")
	profiles := NewModel(database, "profiles")
	if err := profiles.Getter("status", func(interface{}, map[string]interface{}) interface{} { return "enabled" }); err != nil {
		t.Fatalf("注册关联获取器失败: %v", err)
	}
	if err := users.DefineHasMany("profiles", profiles, "user_id", "id"); err != nil {
		t.Fatalf("定义关联失败: %v", err)
	}
	rows, err := users.With("profiles").Select()
	if err != nil {
		t.Fatalf("预加载关联失败: %v", err)
	}
	for index, expectedLabel := range []string{"numeric", "string"} {
		related := rows[index]["profiles"].([]map[string]interface{})
		if len(related) != 1 || related[0]["label"] != expectedLabel || related[0]["status"] != "enabled" {
			t.Fatalf("第 %d 行关联分组或获取器错误: %#v", index, related)
		}
	}
}

// TestRelationLoadingRejectsMissingKeys 验证字段裁剪导致关联键缺失时不会静默返回空关联。
func TestRelationLoadingRejectsMissingKeys(t *testing.T) {
	connection := &relationHardeningConnection{missingParentKey: true}
	database := NewDB(connection)
	users := NewModel(database, "users")
	profiles := NewModel(database, "profiles")
	if err := users.DefineHasMany("profiles", profiles, "user_id", "id"); err != nil {
		t.Fatalf("定义关联失败: %v", err)
	}
	if _, err := users.With("profiles").Select(); !errors.Is(err, ErrInvalidRelation) {
		t.Fatalf("缺失本地关联键应返回 ErrInvalidRelation，实际为 %v", err)
	}
}
