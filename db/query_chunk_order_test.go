package db

import (
	"errors"
	"slices"
	"testing"
)

func TestQueryChunkRejectsMissingOrderBeforeSelecting(t *testing.T) {
	connection := &chunkRecorderConn{}
	database := NewDB(connection)
	called := false
	err := database.Table("records").Chunk(2, func([]map[string]any) bool {
		called = true
		return true
	})
	if !errors.Is(err, ErrInvalidQuery) || !errors.Is(err, ErrChunkOrderRequired) || called || len(connection.selects) != 0 {
		t.Fatalf("没有排序时应在读取前报错: %v, callbacks=%t, selects=%d", err, called, len(connection.selects))
	}
}

func TestQueryChunkKeepsOrderForEveryNonStreamingPage(t *testing.T) {
	connection := &chunkRecorderConn{pages: [][]map[string]any{
		{{"code": "a"}, {"code": "b"}},
		{{"code": "c"}},
	}}
	var codes []string
	err := NewDB(connection).Table("records").Order("code ASC").Chunk(2, func(rows []map[string]any) bool {
		for _, row := range rows {
			codes = append(codes, row["code"].(string))
		}
		return true
	})
	if err != nil || !slices.Equal(codes, []string{"a", "b", "c"}) ||
		!slices.Equal(connection.orders, []string{"code ASC", "code ASC"}) ||
		!slices.Equal(connection.offsets, []int{0, 2}) {
		t.Fatalf("分块未将唯一顺序传给每次查询: codes=%v orders=%v offsets=%v err=%v", codes, connection.orders, connection.offsets, err)
	}
}

func TestQueryChunkWithUniqueTieBreakerSurvivesSQLiteIndexChange(t *testing.T) {
	database, raw := newModelScanSQLite(t)
	if _, err := raw.Exec(`CREATE TABLE chunk_records (code TEXT PRIMARY KEY NOT NULL, score INTEGER NOT NULL);
		INSERT INTO chunk_records(code, score) VALUES ('c', 2), ('b', 1), ('a', 1), ('d', 2), ('f', 3), ('e', 3);`); err != nil {
		t.Fatal(err)
	}
	var codes []string
	var createIndexErr error
	err := database.Table("chunk_records").Where("score > ?", 0).Order("score ASC, code ASC").
		Chunk(2, func(rows []map[string]any) bool {
			if len(codes) == 0 {
				_, createIndexErr = raw.Exec("CREATE INDEX chunk_records_score ON chunk_records(score, code)")
			}
			for _, row := range rows {
				codes = append(codes, row["code"].(string))
			}
			return createIndexErr == nil
		})
	if err != nil || createIndexErr != nil || !slices.Equal(codes, []string{"a", "b", "c", "d", "e", "f"}) {
		t.Fatalf("稳定排序在索引变更后出现遗漏或重复: %v, %v, %v", codes, err, createIndexErr)
	}
}
