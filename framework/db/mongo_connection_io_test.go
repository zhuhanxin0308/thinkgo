package db

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/event"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/drivertest"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/xoptions"
)

// newMongoMockConnection 使用驱动提供的模拟部署创建隔离连接，测试结束后主动释放客户端资源。
func newMongoMockConnection(t *testing.T, responses ...bson.D) *MongoConnection {
	return newMongoMockConnectionWithMonitor(t, nil, responses...)
}

func newMongoMockConnectionWithMonitor(t *testing.T, monitor *event.CommandMonitor, responses ...bson.D) *MongoConnection {
	t.Helper()

	deployment := drivertest.NewMockDeployment(responses...)
	clientOptions := options.Client().SetMonitor(monitor)
	if err := xoptions.SetInternalClientOptions(clientOptions, "deployment", deployment); err != nil {
		t.Fatalf("配置 MongoDB 模拟部署失败: %v", err)
	}
	client, err := mongo.Connect(clientOptions)
	if err != nil {
		t.Fatalf("创建 MongoDB 模拟客户端失败: %v", err)
	}
	t.Cleanup(func() {
		if err := client.Disconnect(context.Background()); err != nil {
			t.Errorf("关闭 MongoDB 模拟客户端失败: %v", err)
		}
	})

	return &MongoConnection{Client: client, Database: "app"}
}

type mongoModelLoopUser struct {
	ID   string `thinkgo:"id"`
	Name string `thinkgo:"name"`
}

type mongoExplicitIDUser struct {
	ID   string `thinkgo:"_id"`
	Name string `thinkgo:"name"`
}

type mongoCommandRecorder struct {
	mu       sync.Mutex
	commands map[string]bson.Raw
}

func (recorder *mongoCommandRecorder) monitor() *event.CommandMonitor {
	return &event.CommandMonitor{Started: func(_ context.Context, started *event.CommandStartedEvent) {
		recorder.mu.Lock()
		recorder.commands[started.CommandName] = append(bson.Raw(nil), started.Command...)
		recorder.mu.Unlock()
	}}
}

func (recorder *mongoCommandRecorder) filter(t *testing.T, commandName string) bson.M {
	t.Helper()
	recorder.mu.Lock()
	command := append(bson.Raw(nil), recorder.commands[commandName]...)
	recorder.mu.Unlock()
	if len(command) == 0 {
		t.Fatalf("MongoDB command %q was not recorded", commandName)
	}

	var filter bson.Raw
	switch commandName {
	case "find":
		filter = command.Lookup("filter").Document()
	case "update":
		filter = command.Lookup("updates").Array().Index(0).Document().Lookup("q").Document()
	case "delete":
		filter = command.Lookup("deletes").Array().Index(0).Document().Lookup("q").Document()
	default:
		t.Fatalf("unsupported recorded command %q", commandName)
	}
	var decoded bson.M
	if err := bson.Unmarshal(filter, &decoded); err != nil {
		t.Fatalf("decode MongoDB %s filter: %v", commandName, err)
	}
	return decoded
}

func assertMongoModelUsesIntrinsicID(t *testing.T, recorder *mongoCommandRecorder, commandName string) {
	t.Helper()
	filter := recorder.filter(t, commandName)
	if _, exists := filter["_id"]; !exists {
		t.Fatalf("MongoDB Model %s must filter by intrinsic _id: %#v", commandName, filter)
	}
	if _, exists := filter["id"]; exists {
		t.Fatalf("MongoDB Model %s leaked logical id into storage filter: %#v", commandName, filter)
	}
}

func TestMongoModelDefaultIDRoundTripsThroughIntrinsicID(t *testing.T) {
	objectID := bson.NewObjectID()
	recorder := &mongoCommandRecorder{commands: make(map[string]bson.Raw)}
	connection := newMongoMockConnectionWithMonitor(t, recorder.monitor(),
		mongoMockSuccessResponse(bson.E{Key: "n", Value: int32(1)}),
		mongoMockSuccessResponse(bson.E{Key: "n", Value: int32(1)}, bson.E{Key: "nModified", Value: int32(1)}),
		mongoMockCursorResponse("app.users", bson.D{{Key: "_id", Value: objectID}, {Key: "name", Value: "Grace"}}),
		mongoMockSuccessResponse(bson.E{Key: "n", Value: int32(1)}),
	)
	model := NewModel(NewDB(connection), "users")
	user := &mongoModelLoopUser{Name: "Ada"}

	if err := model.Create(user); err != nil {
		t.Fatalf("MongoDB default Model create failed: %v", err)
	}
	if user.ID == "" {
		t.Fatal("MongoDB default Model must bind generated _id into logical ID")
	}
	user.Name = "Grace"
	if err := model.Save(user); err != nil {
		t.Fatalf("MongoDB default Model save failed: %v", err)
	}
	assertMongoModelUsesIntrinsicID(t, recorder, "update")

	row, err := model.Where("id", "=", user.ID).Find()
	if err != nil {
		t.Fatalf("MongoDB default Model find failed: %v", err)
	}
	if row["id"] != objectID.Hex() {
		t.Fatalf("MongoDB intrinsic _id must normalize to logical id: %#v", row)
	}
	if _, exists := row["_id"]; exists {
		t.Fatalf("MongoDB default Model must not expose duplicate storage _id: %#v", row)
	}
	assertMongoModelUsesIntrinsicID(t, recorder, "find")

	deleted, err := model.Where("id", "=", user.ID).Delete()
	if err != nil || deleted != 1 {
		t.Fatalf("MongoDB default Model delete failed: deleted=%d err=%v", deleted, err)
	}
	assertMongoModelUsesIntrinsicID(t, recorder, "delete")
}

func TestMongoModelExplicitIntrinsicAndBusinessPrimaryKeysRemainStable(t *testing.T) {
	t.Run("explicit intrinsic key", func(t *testing.T) {
		recorder := &mongoCommandRecorder{commands: make(map[string]bson.Raw)}
		connection := newMongoMockConnectionWithMonitor(t, recorder.monitor(),
			mongoMockSuccessResponse(bson.E{Key: "n", Value: int32(1)}),
			mongoMockSuccessResponse(bson.E{Key: "n", Value: int32(1)}, bson.E{Key: "nModified", Value: int32(1)}),
		)
		model := NewModel(NewDB(connection), "users").PrimaryKey("_id")
		user := &mongoExplicitIDUser{Name: "Ada"}
		if err := model.Create(user); err != nil || user.ID == "" {
			t.Fatalf("explicit MongoDB _id create failed: value=%#v err=%v", user, err)
		}
		user.Name = "Grace"
		if err := model.Save(user); err != nil {
			t.Fatalf("explicit MongoDB _id save failed: %v", err)
		}
		assertMongoModelUsesIntrinsicID(t, recorder, "update")
	})

	t.Run("explicit business key", func(t *testing.T) {
		businessID := bson.NewObjectID()
		recorder := &mongoCommandRecorder{commands: make(map[string]bson.Raw)}
		connection := newMongoMockConnectionWithMonitor(t, recorder.monitor(),
			mongoMockSuccessResponse(bson.E{Key: "n", Value: int32(1)}),
			mongoMockSuccessResponse(bson.E{Key: "n", Value: int32(1)}, bson.E{Key: "nModified", Value: int32(1)}),
		)
		model := NewModel(NewDB(connection), "users").PrimaryKey("id")
		user := &mongoModelLoopUser{ID: businessID.Hex(), Name: "Ada"}
		if err := model.Create(user); err != nil {
			t.Fatalf("explicit MongoDB business-key create failed: %v", err)
		}
		user.Name = "Grace"
		if err := model.Save(user); err != nil {
			t.Fatalf("explicit MongoDB business-key save failed: %v", err)
		}
		filter := recorder.filter(t, "update")
		if filter["id"] != businessID {
			t.Fatalf("explicit business primary key must remain id: %#v", filter)
		}
		if _, exists := filter["_id"]; exists {
			t.Fatalf("explicit business primary key must not be remapped to _id: %#v", filter)
		}
	})

	t.Run("zero explicit business key", func(t *testing.T) {
		recorder := &mongoCommandRecorder{commands: make(map[string]bson.Raw)}
		connection := newMongoMockConnectionWithMonitor(t, recorder.monitor(),
			mongoMockSuccessResponse(bson.E{Key: "n", Value: int32(1)}),
		)
		model := NewModel(NewDB(connection), "users").PrimaryKey("id")
		user := &mongoModelLoopUser{Name: "Ada"}
		err := model.Create(user)
		if !errors.Is(err, ErrInvalidModel) {
			t.Fatalf("zero explicit MongoDB business key must fail before insert: value=%#v err=%v", user, err)
		}
		recorder.mu.Lock()
		_, inserted := recorder.commands["insert"]
		recorder.mu.Unlock()
		if inserted {
			t.Fatal("zero explicit MongoDB business key reached the driver")
		}
	})
}

func TestMongoModelRejectsIntrinsicPrimaryKeyUpdates(t *testing.T) {
	tests := map[string]map[string]interface{}{
		"logical alias": {"id": bson.NewObjectID().Hex()},
		"storage key":   {"_id": bson.NewObjectID()},
		"alias collision": {
			"id":  bson.NewObjectID().Hex(),
			"_id": bson.NewObjectID(),
		},
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			recorder := &mongoCommandRecorder{commands: make(map[string]bson.Raw)}
			connection := newMongoMockConnectionWithMonitor(t, recorder.monitor(),
				mongoMockSuccessResponse(bson.E{Key: "n", Value: int32(1)}, bson.E{Key: "nModified", Value: int32(1)}),
			)
			model := NewModel(NewDB(connection), "users")
			_, err := model.Where("id", bson.NewObjectID().Hex()).Update(data)
			if !errors.Is(err, ErrInvalidQuery) {
				t.Fatalf("MongoDB intrinsic primary key update must be rejected: %v", err)
			}
			recorder.mu.Lock()
			_, updated := recorder.commands["update"]
			recorder.mu.Unlock()
			if updated {
				t.Fatal("MongoDB intrinsic primary key update reached the driver")
			}
		})
	}
}

// mongoMockCursorResponse 构造符合 MongoDB 线协议的首批游标响应。
func mongoMockCursorResponse(namespace string, documents ...bson.D) bson.D {
	batch := make(bson.A, len(documents))
	for index, document := range documents {
		batch[index] = document
	}
	return bson.D{
		{Key: "ok", Value: 1},
		{Key: "cursor", Value: bson.D{
			{Key: "id", Value: int64(0)},
			{Key: "ns", Value: namespace},
			{Key: "firstBatch", Value: batch},
		}},
	}
}

// mongoMockSuccessResponse 构造带业务字段的成功命令响应。
func mongoMockSuccessResponse(elements ...bson.E) bson.D {
	return append(bson.D{{Key: "ok", Value: 1}}, elements...)
}

// mongoMockCommandErrorResponse 构造服务端命令失败响应。
func mongoMockCommandErrorResponse(code int32, message string) bson.D {
	return bson.D{
		{Key: "ok", Value: 0},
		{Key: "code", Value: code},
		{Key: "errmsg", Value: message},
	}
}

// TestMongoConnectionMockedIO 使用 MongoDB 官方模拟部署验证查询、写入和统计路径，
// 确保框架生成的命令经过真实驱动编码并正确解析驱动响应。
func TestMongoConnectionMockedIO(t *testing.T) {
	t.Run("查询并规范化文档", func(t *testing.T) {
		objectID := bson.NewObjectID()
		connection := newMongoMockConnection(t, mongoMockCursorResponse("app.users",
			bson.D{
				{Key: "_id", Value: objectID},
				{Key: "name", Value: "张三"},
				{Key: "profile", Value: bson.D{{Key: "level", Value: int32(3)}}},
				{Key: "tags", Value: bson.A{"admin", int32(7)}},
			},
		))

		rows, err := connection.Select(context.Background(), newSelectRequest(
			"users", "name,profile,tags", mustTestPredicate(t, []string{"age >= ?"}, []interface{}{18}), "_id", "name DESC", 10, 2, nil,
		))
		if err != nil {
			t.Fatalf("MongoDB 模拟查询失败: %v", err)
		}
		if len(rows) != 1 || rows[0]["name"] != "张三" {
			t.Fatalf("MongoDB 查询结果错误: %#v", rows)
		}
		if _, exists := rows[0]["_id"]; exists {
			t.Fatalf("显式投影未选择 _id 时结果必须排除该字段: %#v", rows[0])
		}
		profile, ok := rows[0]["profile"].(map[string]interface{})
		if !ok || profile["level"] != int32(3) {
			t.Fatalf("嵌套文档未规范化: %#v", rows[0]["profile"])
		}
		tags, ok := rows[0]["tags"].([]interface{})
		if !ok || !reflect.DeepEqual(tags, []interface{}{"admin", int32(7)}) {
			t.Fatalf("数组未规范化: %#v", rows[0]["tags"])
		}
	})

	t.Run("插入不修改调用方数据", func(t *testing.T) {
		connection := newMongoMockConnection(t, mongoMockSuccessResponse(bson.E{Key: "n", Value: int32(1)}))
		data := map[string]interface{}{"name": "张三", "active": true}
		before := cloneDatabaseMap(data)
		result, err := connection.Insert(context.Background(), newInsertRequest("users", data, "_id", false))
		affected := result.Affected
		if err != nil || affected != 1 {
			t.Fatalf("MongoDB 插入结果错误: affected=%d err=%v", affected, err)
		}
		if !reflect.DeepEqual(data, before) {
			t.Fatalf("插入不得修改调用方数据: before=%#v after=%#v", before, data)
		}
	})

	t.Run("批量更新与删除返回真实数量", func(t *testing.T) {
		connection := newMongoMockConnection(t,
			mongoMockSuccessResponse(bson.E{Key: "n", Value: int32(3)}, bson.E{Key: "nModified", Value: int32(2)}),
			mongoMockSuccessResponse(bson.E{Key: "n", Value: int32(3)}),
		)
		updatedResult, err := connection.Update(context.Background(), newUpdateRequest(
			"users", map[string]interface{}{"active": false}, mustTestPredicate(t, []string{"role = ?"}, []interface{}{"guest"}), "_id",
		))
		updated := updatedResult.Count()
		if err != nil || updated != 2 {
			t.Fatalf("MongoDB 更新结果错误: affected=%d err=%v", updated, err)
		}
		deleteResult, err := connection.Delete(context.Background(), newDeleteRequest(
			"users", mustTestPredicate(t, []string{"active = ?"}, []interface{}{false}), "_id", false,
		))
		deleted := deleteResult.Deleted
		if err != nil || deleted != 3 {
			t.Fatalf("MongoDB 删除结果错误: affected=%d err=%v", deleted, err)
		}
	})

	t.Run("聚合计数", func(t *testing.T) {
		connection := newMongoMockConnection(t, mongoMockCursorResponse("app.users", bson.D{{Key: "n", Value: int32(4)}}))
		count, err := connection.Count(context.Background(), newCountRequest(
			"users", mustTestPredicate(t, []string{"active = ?"}, []interface{}{true}), "_id",
		))
		if err != nil || count != 4 {
			t.Fatalf("MongoDB 计数结果错误: count=%d err=%v", count, err)
		}
	})

	t.Run("驱动错误向上传递", func(t *testing.T) {
		connection := newMongoMockConnection(t, mongoMockCommandErrorResponse(91, "shutdown in progress"))
		if _, err := connection.Select(context.Background(), newSelectRequest("users", "*", newPredicate(), "_id", "", 0, 0, nil)); err == nil {
			t.Fatal("MongoDB 驱动错误必须向上传递")
		}
	})
}

// TestMongoConnectionSelectEachStreamsAndStops 验证 MongoDB 流式读取按行回调并支持提前停止。
func TestMongoConnectionSelectEachStreamsAndStops(t *testing.T) {
	connection := newMongoMockConnection(t, mongoMockCursorResponse("app.users",
		bson.D{{Key: "name", Value: "Ada"}},
		bson.D{{Key: "name", Value: "Grace"}},
	))
	var rows []map[string]interface{}
	err := connection.SelectEach(context.Background(), newSelectRequest(
		"users", "name", newPredicate(), "_id", "name ASC", 0, 0, nil,
	), func(row map[string]interface{}) bool {
		rows = append(rows, row)
		return false
	})
	if err != nil {
		t.Fatalf("MongoDB 流式查询失败: %v", err)
	}
	if len(rows) != 1 || rows[0]["name"] != "Ada" {
		t.Fatalf("MongoDB 流式查询未按回调停止: %#v", rows)
	}
}

// TestQueryEachUsesMongoStreamingConnection 验证统一 Query.Each 会路由到 MongoDB 游标，而不是返回 SQL 专属错误。
func TestQueryEachUsesMongoStreamingConnection(t *testing.T) {
	connection := newMongoMockConnection(t, mongoMockCursorResponse("app.users",
		bson.D{{Key: "name", Value: "Ada"}},
		bson.D{{Key: "name", Value: "Grace"}},
	))
	database := NewDB(connection)
	count := 0
	err := database.Name("users").Field("name").Each(func(row map[string]interface{}) bool {
		count++
		return row["name"] != "Grace"
	})
	if err != nil {
		t.Fatalf("Query.Each 未支持 MongoDB 流式查询: %v", err)
	}
	if count != 2 {
		t.Fatalf("Query.Each 回调次数错误: %d", count)
	}
}

func TestMongoOperationResultsPreserveIDAndCounts(t *testing.T) {
	objectID := bson.NewObjectID()
	connection := newMongoMockConnection(t,
		mongoMockSuccessResponse(bson.E{Key: "n", Value: int32(1)}),
		mongoMockSuccessResponse(bson.E{Key: "n", Value: int32(3)}, bson.E{Key: "nModified", Value: int32(2)}),
		mongoMockSuccessResponse(bson.E{Key: "n", Value: int32(3)}),
	)

	inserted, err := connection.Insert(context.Background(), newInsertRequest(
		"users", map[string]interface{}{"_id": objectID, "name": "Ada"}, "_id", true,
	))
	if err != nil {
		t.Fatalf("MongoDB typed insert failed: %v", err)
	}
	if inserted.Affected != 1 || !inserted.IDKnown || inserted.ID != objectID {
		t.Fatalf("MongoDB insert result lost id or affected count: %#v", inserted)
	}

	predicate := newPredicate().appendValidated("role = ?", []interface{}{"guest"})
	updated, err := connection.Update(context.Background(), newUpdateRequest(
		"users", map[string]interface{}{"active": false}, predicate, "_id",
	))
	if err != nil {
		t.Fatalf("MongoDB typed update failed: %v", err)
	}
	if updated.Affected != 2 || updated.Matched != 3 || updated.Modified != 2 || !updated.MatchedKnown || !updated.ModifiedKnown {
		t.Fatalf("MongoDB update result lost matched/modified counts: %#v", updated)
	}

	deleted, err := connection.Delete(context.Background(), newDeleteRequest("users", predicate, "_id", false))
	if err != nil {
		t.Fatalf("MongoDB typed delete failed: %v", err)
	}
	if deleted.Deleted != 3 {
		t.Fatalf("MongoDB delete result lost deleted count: %#v", deleted)
	}
}

func TestMongoInsertCoercesOnlyDeclaredPrimaryKey(t *testing.T) {
	objectID := bson.NewObjectID()
	connection := newMongoMockConnection(t, mongoMockSuccessResponse(bson.E{Key: "n", Value: int32(1)}))
	inserted, err := connection.Insert(context.Background(), newInsertRequest(
		"users",
		map[string]interface{}{"_id": objectID.Hex(), "external_code": objectID.Hex()},
		"_id",
		true,
	))
	if err != nil {
		t.Fatal(err)
	}
	if inserted.ID != objectID || inserted.Data["_id"] != objectID || inserted.Data["external_code"] != objectID.Hex() {
		t.Fatalf("MongoDB insert primary-key conversion escaped its declared path: %#v", inserted)
	}
}

func TestMongoOperationRejectsExplicitRawPredicate(t *testing.T) {
	connection := newMongoMockConnection(t)
	predicate := newPredicate().appendRaw("role = ?", []interface{}{"guest"})
	_, err := connection.Update(context.Background(), newUpdateRequest(
		"users", map[string]interface{}{"active": false}, predicate, "_id",
	))
	if !errors.Is(err, ErrUnsafeExpression) {
		t.Fatalf("MongoDB raw predicate must be rejected before driver access, got %v", err)
	}
}

// TestMongoConnectionValidationBoundaries 验证无效上下文、危险全量操作和非法输入在访问驱动前失败。
func TestMongoConnectionValidationBoundaries(t *testing.T) {
	connection := &MongoConnection{}
	// 类型化空上下文用于验证异常输入，生产调用必须传递有效上下文。
	var nilContext context.Context
	if _, err := connection.Select(nilContext, newSelectRequest("users", "*", newPredicate(), "_id", "", 0, 0, nil)); !errors.Is(err, ErrDatabaseUnavailable) {
		t.Fatalf("不可用连接应优先返回 ErrDatabaseUnavailable，实际为 %v", err)
	}
	if _, err := connection.Update(context.Background(), newUpdateRequest("users", map[string]interface{}{"active": false}, newPredicate(), "_id")); !errors.Is(err, ErrUnsafeFullTableMutation) {
		t.Fatalf("无条件更新应返回 ErrUnsafeFullTableMutation，实际为 %v", err)
	}
	if _, err := connection.Delete(context.Background(), newDeleteRequest("users", newPredicate(), "_id", false)); !errors.Is(err, ErrUnsafeFullTableMutation) {
		t.Fatalf("无条件删除应返回 ErrUnsafeFullTableMutation，实际为 %v", err)
	}
	if err := connection.Close(); !errors.Is(err, ErrDatabaseUnavailable) {
		t.Fatalf("关闭不可用连接应返回 ErrDatabaseUnavailable，实际为 %v", err)
	}

	t.Run("输入校验", func(t *testing.T) {
		valid := newMongoMockConnection(t)
		if _, err := valid.Select(nilContext, newSelectRequest("users", "*", newPredicate(), "_id", "", 0, 0, nil)); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("空上下文应返回 ErrInvalidQuery，实际为 %v", err)
		}
		if _, err := valid.Select(context.Background(), newSelectRequest("users", "*", newPredicate(), "_id", "", -1, 0, nil)); !errors.Is(err, ErrInvalidPagination) {
			t.Fatalf("负数分页应返回 ErrInvalidPagination，实际为 %v", err)
		}
		if _, err := valid.Insert(context.Background(), newInsertRequest("users", nil, "_id", false)); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("空文档插入应返回 ErrInvalidQuery，实际为 %v", err)
		}
	})
}
