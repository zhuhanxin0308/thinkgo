package db

import (
	"context"
	"reflect"
	"testing"
)

const (
	modelRecordBenchmarkTable = "benchmark_records"
	modelRecordBenchmarkID    = int64(7)
	modelRecordBenchmarkName  = "Ada"
)

type modelRecordBenchmarkDTO struct {
	ID     int64  `thinkgo:"id"`
	Name   string `thinkgo:"name"`
	Email  string `thinkgo:"email"`
	Status int64  `thinkgo:"status"`
}

type modelRecordBenchmarkOwner struct {
	*Model
	ID     int64  `thinkgo:"id"`
	Name   string `thinkgo:"name"`
	Email  string `thinkgo:"email"`
	Status int64  `thinkgo:"status"`
}

// modelRecordBenchmarkConnection 每次返回独立数据行，只测框架路径和结果分配，不引入数据库或网络延迟。
type modelRecordBenchmarkConnection struct {
	connectionIdentityState
}

func (*modelRecordBenchmarkConnection) Select(ctx context.Context, request SelectRequest) ([]map[string]interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if request.Table() != modelRecordBenchmarkTable {
		return nil, ErrInvalidQuery
	}
	return []map[string]interface{}{{
		"id": modelRecordBenchmarkID, "name": modelRecordBenchmarkName,
		"email": "ada@example.test", "status": int64(1),
	}}, nil
}

// 未变更保存不应触发数据库写入，所有写接口明确失败以防基准场景悄悄改变。
func (*modelRecordBenchmarkConnection) Insert(context.Context, InsertRequest) (InsertResult, error) {
	return InsertResult{}, ErrUnsupportedFeature
}

func (*modelRecordBenchmarkConnection) Update(context.Context, UpdateRequest) (UpdateResult, error) {
	return UpdateResult{}, ErrUnsupportedFeature
}

func (*modelRecordBenchmarkConnection) Delete(context.Context, DeleteRequest) (DeleteResult, error) {
	return DeleteResult{}, ErrUnsupportedFeature
}

func (*modelRecordBenchmarkConnection) Count(context.Context, CountRequest) (int64, error) {
	return 0, ErrUnsupportedFeature
}

func (*modelRecordBenchmarkConnection) Close() error { return nil }

func newModelRecordBenchmarkDatabase(b *testing.B) *DB {
	b.Helper()
	database := NewDB(&modelRecordBenchmarkConnection{})
	b.Cleanup(func() {
		if err := database.Close(); err != nil {
			b.Errorf("关闭模型基准数据库失败: %v", err)
		}
	})
	return database
}

// BenchmarkModelRecordConstruction 测量元数据预热后独立模型构造和拥有者注入的成本。
func BenchmarkModelRecordConstruction(b *testing.B) {
	database := newModelRecordBenchmarkDatabase(b)
	ctx := context.Background()
	if err := PrewarmModelMetadata(reflect.TypeFor[modelRecordBenchmarkOwner]()); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		owner := &modelRecordBenchmarkOwner{}
		model, err := NewModelFor(ctx, database, owner)
		if err != nil || owner.Model != model {
			b.Fatalf("模型构造或注入失败: %v", err)
		}
	}
}

// BenchmarkModelRecordSaveUnchanged 测量已加载记录的身份检查与变更检查，数据库写入必须始终为零。
func BenchmarkModelRecordSaveUnchanged(b *testing.B) {
	database := newModelRecordBenchmarkDatabase(b)
	query := NewModel(database, modelRecordBenchmarkTable).Where("id", modelRecordBenchmarkID)
	var owner modelRecordBenchmarkOwner
	if found, err := query.Find(&owner); err != nil || !found {
		b.Fatalf("加载模型基准记录失败: found=%v err=%v", found, err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := owner.Save(); err != nil {
			b.Fatalf("未修改记录不应触发写入: %v", err)
		}
	}
}

// BenchmarkModelRecordFind 使用相同查询、四列数据和纯内存连接，比较动态行、普通结构体及记录绑定。
// 结果包括公共查询与取消边界，不能作为真实数据库吞吐或跨框架性能排名。
func BenchmarkModelRecordFind(b *testing.B) {
	if err := PrewarmModelMetadata(reflect.TypeFor[modelRecordBenchmarkDTO](), reflect.TypeFor[modelRecordBenchmarkOwner]()); err != nil {
		b.Fatal(err)
	}
	b.Run("FindMap", func(b *testing.B) {
		query := NewModel(newModelRecordBenchmarkDatabase(b), modelRecordBenchmarkTable).Where("id", modelRecordBenchmarkID)
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			row, err := query.FindMap()
			if err != nil || row["id"] != modelRecordBenchmarkID || row["name"] != modelRecordBenchmarkName {
				b.Fatalf("动态模型查询失败: row=%#v err=%v", row, err)
			}
		}
	})
	b.Run("DTO", func(b *testing.B) {
		query := NewModel(newModelRecordBenchmarkDatabase(b), modelRecordBenchmarkTable).Where("id", modelRecordBenchmarkID)
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			var target modelRecordBenchmarkDTO
			if found, err := query.Find(&target); err != nil || !found || target.ID != modelRecordBenchmarkID || target.Name != modelRecordBenchmarkName {
				b.Fatalf("结构体模型查询失败: target=%#v found=%v err=%v", target, found, err)
			}
		}
	})
	b.Run("Record", func(b *testing.B) {
		query := NewModel(newModelRecordBenchmarkDatabase(b), modelRecordBenchmarkTable).Where("id", modelRecordBenchmarkID)
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			var target modelRecordBenchmarkOwner
			if found, err := query.Find(&target); err != nil || !found || target.Model == nil || target.ID != modelRecordBenchmarkID || target.Name != modelRecordBenchmarkName {
				b.Fatalf("记录模型查询或绑定失败: target=%#v found=%v err=%v", target, found, err)
			}
		}
	})
}
