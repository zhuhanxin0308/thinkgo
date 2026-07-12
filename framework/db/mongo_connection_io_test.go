package db

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/drivertest"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/xoptions"
)

// newMongoMockConnection 使用驱动提供的模拟部署创建隔离连接，测试结束后主动释放客户端资源。
func newMongoMockConnection(t *testing.T, responses ...bson.D) *MongoConnection {
	t.Helper()

	deployment := drivertest.NewMockDeployment(responses...)
	clientOptions := options.Client()
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

		rows, err := connection.Select("users", "name,profile,tags", []string{"age >= ?"}, []interface{}{18}, "name DESC", 10, 2)
		if err != nil {
			t.Fatalf("MongoDB 模拟查询失败: %v", err)
		}
		if len(rows) != 1 || rows[0]["_id"] != objectID.Hex() || rows[0]["name"] != "张三" {
			t.Fatalf("MongoDB 查询结果错误: %#v", rows)
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
		affected, err := connection.Insert("users", data)
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
		updated, err := connection.Update("users", map[string]interface{}{"active": false}, []string{"role = ?"}, []interface{}{"guest"})
		if err != nil || updated != 2 {
			t.Fatalf("MongoDB 更新结果错误: affected=%d err=%v", updated, err)
		}
		deleted, err := connection.Delete("users", []string{"active = ?"}, []interface{}{false})
		if err != nil || deleted != 3 {
			t.Fatalf("MongoDB 删除结果错误: affected=%d err=%v", deleted, err)
		}
	})

	t.Run("聚合计数", func(t *testing.T) {
		connection := newMongoMockConnection(t, mongoMockCursorResponse("app.users", bson.D{{Key: "n", Value: int32(4)}}))
		count, err := connection.Count("users", []string{"active = ?"}, []interface{}{true})
		if err != nil || count != 4 {
			t.Fatalf("MongoDB 计数结果错误: count=%d err=%v", count, err)
		}
	})

	t.Run("驱动错误向上传递", func(t *testing.T) {
		connection := newMongoMockConnection(t, mongoMockCommandErrorResponse(91, "shutdown in progress"))
		if _, err := connection.Select("users", "*", nil, nil, "", 0, 0); err == nil {
			t.Fatal("MongoDB 驱动错误必须向上传递")
		}
	})
}

// TestMongoConnectionValidationBoundaries 验证无效上下文、危险全量操作和非法输入在访问驱动前失败。
func TestMongoConnectionValidationBoundaries(t *testing.T) {
	connection := &MongoConnection{}
	// 类型化空上下文用于验证异常输入，生产调用必须传递有效上下文。
	var nilContext context.Context
	if _, err := connection.SelectContext(nilContext, "users", "*", nil, nil, "", 0, 0); !errors.Is(err, ErrDatabaseUnavailable) {
		t.Fatalf("不可用连接应优先返回 ErrDatabaseUnavailable，实际为 %v", err)
	}
	if _, err := connection.Update("users", map[string]interface{}{"active": false}, nil, nil); !errors.Is(err, ErrUnsafeFullTableMutation) {
		t.Fatalf("无条件更新应返回 ErrUnsafeFullTableMutation，实际为 %v", err)
	}
	if _, err := connection.Delete("users", nil, nil); !errors.Is(err, ErrUnsafeFullTableMutation) {
		t.Fatalf("无条件删除应返回 ErrUnsafeFullTableMutation，实际为 %v", err)
	}
	if err := connection.Close(); !errors.Is(err, ErrDatabaseUnavailable) {
		t.Fatalf("关闭不可用连接应返回 ErrDatabaseUnavailable，实际为 %v", err)
	}

	t.Run("输入校验", func(t *testing.T) {
		valid := newMongoMockConnection(t)
		if _, err := valid.SelectContext(nilContext, "users", "*", nil, nil, "", 0, 0); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("空上下文应返回 ErrInvalidQuery，实际为 %v", err)
		}
		if _, err := valid.SelectContext(context.Background(), "users", "*", nil, nil, "", -1, 0); !errors.Is(err, ErrInvalidPagination) {
			t.Fatalf("负数分页应返回 ErrInvalidPagination，实际为 %v", err)
		}
		if _, err := valid.Insert("users", nil); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("空文档插入应返回 ErrInvalidQuery，实际为 %v", err)
		}
	})
}
