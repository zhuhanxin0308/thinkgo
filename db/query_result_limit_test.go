package db

import (
	"context"
	"errors"
	"testing"
)

type oversizedResultConnection struct {
	connectionIdentityState
	rows []map[string]interface{}
}

func (connection *oversizedResultConnection) Select(context.Context, SelectRequest) ([]map[string]interface{}, error) {
	return connection.rows, nil
}

func (*oversizedResultConnection) Insert(context.Context, InsertRequest) (InsertResult, error) {
	return InsertResult{}, nil
}

func (*oversizedResultConnection) Update(context.Context, UpdateRequest) (UpdateResult, error) {
	return UpdateResult{}, nil
}

func (*oversizedResultConnection) Delete(context.Context, DeleteRequest) (DeleteResult, error) {
	return DeleteResult{}, nil
}

func (*oversizedResultConnection) Count(context.Context, CountRequest) (int64, error) {
	return 0, nil
}

func (*oversizedResultConnection) Close() error { return nil }

func TestQueryRejectsOversizedMaterializedResult(t *testing.T) {
	rows := make([]map[string]interface{}, maxQueryResultRows+1)
	database := NewDB(&oversizedResultConnection{rows: rows})
	if _, err := database.Table("users").Select(); !errors.Is(err, ErrQueryResultTooMany) {
		t.Fatalf("超大物化结果应返回 ErrQueryResultTooMany，实际为 %v", err)
	}
}
