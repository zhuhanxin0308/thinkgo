package db

import (
	"context"
	"sync"
	"testing"
)

type immutableQueryConnection struct{ connectionIdentityState }

func (*immutableQueryConnection) Select(context.Context, SelectRequest) ([]map[string]interface{}, error) {
	return nil, nil
}
func (*immutableQueryConnection) Insert(_ context.Context, request InsertRequest) (InsertResult, error) {
	return InsertResult{Affected: 1, ID: int64(1), IDKnown: request.WantsID()}, nil
}
func (*immutableQueryConnection) Update(context.Context, UpdateRequest) (UpdateResult, error) {
	return UpdateResult{Affected: 1}, nil
}
func (*immutableQueryConnection) Delete(context.Context, DeleteRequest) (DeleteResult, error) {
	return DeleteResult{Deleted: 1}, nil
}
func (*immutableQueryConnection) Count(context.Context, CountRequest) (int64, error) {
	return 0, nil
}
func (*immutableQueryConnection) Close() error { return nil }

func TestQueryBranchesAreImmutable(t *testing.T) {
	base := NewDB(&immutableQueryConnection{}).Table("users")
	active := base.WhereField("status", "=", 1).Order("id")
	deleted := base.WhereField("deleted", "=", 1).Limit(5)

	if base == active || base == deleted || active == deleted {
		t.Fatal("链式调用必须返回不同 Query 实例")
	}
	if len(base.where) != 0 || base.order != "" || base.limit != 0 {
		t.Fatalf("基础 Query 被污染: %#v", base)
	}
	if len(active.where) != 1 || active.order != "id" || active.limit != 0 {
		t.Fatalf("active 分支状态错误: %#v", active)
	}
	if len(deleted.where) != 1 || deleted.limit != 5 || deleted.order != "" {
		t.Fatalf("deleted 分支状态错误: %#v", deleted)
	}
}

func TestQueryConcurrentDerivationDoesNotRace(t *testing.T) {
	base := NewDB(&immutableQueryConnection{}).Table("users")
	var wait sync.WaitGroup
	for index := 0; index < 64; index++ {
		wait.Add(1)
		go func(id int) {
			defer wait.Done()
			derived := base.WhereField("id", "=", id).Limit(1)
			if len(derived.where) != 1 || derived.limit != 1 {
				t.Errorf("派生查询状态错误: %#v", derived)
			}
		}(index)
	}
	wait.Wait()
	if len(base.where) != 0 || base.limit != 0 {
		t.Fatalf("并发派生污染基础 Query: %#v", base)
	}
}

func TestModelQueryBranchesAreImmutable(t *testing.T) {
	model := NewModel(NewDB(&immutableQueryConnection{}), "users")
	base := model.newModelQuery()
	active := base.WhereField("status", "=", 1).Order("id")
	limited := base.Limit(5)

	if base == active || base == limited || active == limited {
		t.Fatal("ModelQuery 链式调用必须返回独立实例")
	}
	if len(base.query.where) != 0 || base.query.limit != 0 || base.query.order != "" {
		t.Fatalf("基础 ModelQuery 被污染: %#v", base.query)
	}
}

func TestModelSearcherReturnsDerivedQuery(t *testing.T) {
	model := NewModel(NewDB(&immutableQueryConnection{}), "users")
	if err := model.Searcher("name", func(query *Query, value interface{}, data map[string]interface{}) *Query {
		return query.WhereField("name", "=", value)
	}); err != nil {
		t.Fatal(err)
	}
	query := model.WithSearch([]string{"name"}, map[string]interface{}{"name": "Ada"})
	if len(query.query.where) != 1 {
		t.Fatalf("Searcher 返回的派生 Query 未被接收: %#v", query.query)
	}
}
