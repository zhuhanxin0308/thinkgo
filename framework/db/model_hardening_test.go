package db

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"thinkgo/framework/db/builder"
)

type modelHardeningUser struct {
	UserID uint64 `thinkgo:"user_id"`
	Name   string `thinkgo:"name"`
}

func TestModelMetadataCacheIsImmutableAndConcurrent(t *testing.T) {
	typ := reflect.TypeOf(modelHardeningUser{})
	first, err := cachedModelMetadata(typ)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.fields) == 0 {
		t.Fatal("model metadata did not contain exported fields")
	}
	first.fields[0].column = "mutated"
	second, err := cachedModelMetadata(typ)
	if err != nil {
		t.Fatal(err)
	}
	if second.fields[0].column == "mutated" {
		t.Fatal("metadata cache exposed mutable backing data")
	}

	var wait sync.WaitGroup
	errorsFound := make(chan error, 64)
	for worker := 0; worker < 64; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, loadErr := cachedModelMetadata(typ)
			errorsFound <- loadErr
		}()
	}
	wait.Wait()
	close(errorsFound)
	for loadErr := range errorsFound {
		if loadErr != nil {
			t.Fatalf("concurrent metadata load failed: %v", loadErr)
		}
	}
}

type modelPrimaryKeyConnection struct {
	queryHardeningConnection
	insertCalls  atomic.Int64
	updateCalls  atomic.Int64
	insertID     interface{}
	capabilities DriverCapabilities
}

func (c *modelPrimaryKeyConnection) Capabilities() DriverCapabilities {
	return c.capabilities
}

func (c *modelPrimaryKeyConnection) Insert(_ context.Context, request InsertRequest) (InsertResult, error) {
	c.insertCalls.Add(1)
	return InsertResult{
		Affected: 1,
		ID:       c.insertID,
		IDKnown:  request.WantsID(),
		Data:     request.Data(),
	}, nil
}

func (c *modelPrimaryKeyConnection) Update(ctx context.Context, request UpdateRequest) (UpdateResult, error) {
	c.updateCalls.Add(1)
	return c.queryHardeningConnection.Update(ctx, request)
}

type modelSaveUser struct {
	ID   int64  `thinkgo:"id"`
	Name string `thinkgo:"name"`
}

type modelStringKeyUser struct {
	ID   string `thinkgo:"id"`
	Name string `thinkgo:"name"`
}

type modelObjectIDKeyUser struct {
	ID   bson.ObjectID `thinkgo:"id"`
	Name string        `thinkgo:"name"`
}

// TestModelCreateValidatesPrimaryKeyBeforeInsert verifies that a model field
// which cannot hold the driver's identifier is rejected before any write.
func TestModelCreateValidatesPrimaryKeyBeforeInsert(t *testing.T) {
	connection := &modelPrimaryKeyConnection{
		insertID:     int64(9),
		capabilities: DriverCapabilities{InsertIDKinds: []InsertIDKind{InsertIDInteger}},
	}
	model := NewModel(NewDB(connection), "users")
	bad := &struct {
		ID   bool   `thinkgo:"id"`
		Name string `thinkgo:"name"`
	}{Name: "Ada"}

	err := model.Create(bad)
	if !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("incompatible primary key must return ErrInvalidModel: %v", err)
	}
	if calls := connection.insertCalls.Load(); calls != 0 {
		t.Fatalf("invalid primary key must not reach Insert, calls=%d", calls)
	}
}

// TestModelSaveChoosesCreateOrUpdate pins the ThinkPHP-style Save decision:
// zero primary keys create records, while non-zero keys update them.
func TestModelSaveChoosesCreateOrUpdate(t *testing.T) {
	connection := &modelPrimaryKeyConnection{
		insertID:     int64(9),
		capabilities: DriverCapabilities{InsertIDKinds: []InsertIDKind{InsertIDInteger}},
	}
	model := NewModel(NewDB(connection), "users")

	created := &modelSaveUser{Name: "Ada"}
	if err := model.Save(created); err != nil || created.ID != 9 {
		t.Fatalf("Save create branch failed: value=%#v err=%v", created, err)
	}
	updated := &modelSaveUser{ID: 9, Name: "Grace"}
	if err := model.Save(updated); err != nil {
		t.Fatalf("Save update branch failed: %v", err)
	}
	if inserts, updates := connection.insertCalls.Load(), connection.updateCalls.Load(); inserts != 1 || updates != 1 {
		t.Fatalf("Save must choose exactly one write per call: inserts=%d updates=%d", inserts, updates)
	}
}

func TestModelSaveAllowsExistingStringPrimaryKeyWithIntegerIDDriver(t *testing.T) {
	connection := &modelPrimaryKeyConnection{
		insertID:     int64(9),
		capabilities: DriverCapabilities{InsertIDKinds: []InsertIDKind{InsertIDInteger}},
	}
	value := &modelStringKeyUser{ID: "user-9", Name: "Grace"}
	if err := NewModel(NewDB(connection), "users").Save(value); err != nil {
		t.Fatalf("existing application primary key must remain usable for updates: %v", err)
	}
	if inserts, updates := connection.insertCalls.Load(), connection.updateCalls.Load(); inserts != 0 || updates != 1 {
		t.Fatalf("string-key Save must update only: inserts=%d updates=%d", inserts, updates)
	}
}

func TestModelCreatePreservesStringAndObjectIDPrimaryKeys(t *testing.T) {
	objectID := bson.NewObjectID()
	tests := []struct {
		name         string
		insertID     interface{}
		capabilities DriverCapabilities
		value        interface{}
		assert       func(*testing.T, interface{})
	}{
		{
			name:         "string",
			insertID:     "user-9",
			capabilities: DriverCapabilities{InsertIDKinds: []InsertIDKind{InsertIDString}},
			value:        &modelStringKeyUser{Name: "Ada"},
			assert: func(t *testing.T, value interface{}) {
				if actual := value.(*modelStringKeyUser).ID; actual != "user-9" {
					t.Fatalf("string primary key changed type or value: %q", actual)
				}
			},
		},
		{
			name:         "object-id",
			insertID:     objectID,
			capabilities: DriverCapabilities{InsertIDKinds: []InsertIDKind{InsertIDObjectID}},
			value:        &modelObjectIDKeyUser{Name: "Ada"},
			assert: func(t *testing.T, value interface{}) {
				if actual := value.(*modelObjectIDKeyUser).ID; actual != objectID {
					t.Fatalf("ObjectID primary key changed type or value: %v", actual)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			connection := &modelPrimaryKeyConnection{insertID: test.insertID, capabilities: test.capabilities}
			if err := NewModel(NewDB(connection), "users").Create(test.value); err != nil {
				t.Fatalf("Create failed: %v", err)
			}
			test.assert(t, test.value)
		})
	}
}

func TestModelCreateReportsPartialWriteWhenRuntimeIDCannotBind(t *testing.T) {
	connection := &modelPrimaryKeyConnection{
		insertID:     "not-an-integer",
		capabilities: DriverCapabilities{InsertIDKinds: []InsertIDKind{InsertIDDynamic}},
	}
	value := &modelSaveUser{Name: "Ada"}
	err := NewModel(NewDB(connection), "users").Create(value)
	if !errors.Is(err, ErrPartialWrite) {
		t.Fatalf("runtime binding failure must report ErrPartialWrite: %v", err)
	}
	var partial *PartialWriteError
	if !errors.As(err, &partial) || partial.Result.Affected != 1 || partial.Result.ID != "not-an-integer" {
		t.Fatalf("partial write must preserve the typed insert result: %#v", partial)
	}
	if calls := connection.insertCalls.Load(); calls != 1 {
		t.Fatalf("partial write must describe one completed insert, calls=%d", calls)
	}
}

func TestModelInsertAndInsertGetIdReturnDistinctContracts(t *testing.T) {
	connection := &modelPrimaryKeyConnection{
		insertID:     "user-9",
		capabilities: DriverCapabilities{InsertIDKinds: []InsertIDKind{InsertIDString}},
	}
	model := NewModel(NewDB(connection), "users")
	if affected, err := model.Insert(map[string]interface{}{"name": "Ada"}); err != nil || affected != 1 {
		t.Fatalf("Insert must return affected rows: affected=%d err=%v", affected, err)
	}
	if id, err := model.InsertGetId(map[string]interface{}{"name": "Grace"}); err != nil || id != "user-9" {
		t.Fatalf("InsertGetId must preserve the real ID: id=%#v err=%v", id, err)
	}
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
	if _, err := model.InsertGetId(data); err != nil {
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
	connectionIdentityState
	missingParentKey bool
}

func (c *relationHardeningConnection) Select(_ context.Context, request SelectRequest) ([]map[string]interface{}, error) {
	switch request.Table() {
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

func (c *relationHardeningConnection) Insert(_ context.Context, request InsertRequest) (InsertResult, error) {
	return InsertResult{Affected: 1, ID: int64(1), IDKnown: request.WantsID()}, nil
}
func (c *relationHardeningConnection) Update(context.Context, UpdateRequest) (UpdateResult, error) {
	return UpdateResult{Affected: 1}, nil
}
func (c *relationHardeningConnection) Delete(context.Context, DeleteRequest) (DeleteResult, error) {
	return DeleteResult{Deleted: 1}, nil
}
func (c *relationHardeningConnection) Count(context.Context, CountRequest) (int64, error) {
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
