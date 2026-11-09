//go:build cgo

package db

import (
	"database/sql/driver"
	"errors"
	"testing"
)

type snapshotMutableValue struct {
	Bytes  []byte
	Nested map[string][]byte
}

func (value *snapshotMutableValue) Value() (driver.Value, error) {
	return string(value.Bytes) + string(value.Nested["suffix"]), nil
}

// TestDatabaseValuerSnapshotSQLite 验证输入和返回快照都不能改变实际 SQL 的绑定值。
func TestDatabaseValuerSnapshotSQLite(t *testing.T) {
	database, connection := newSQLiteQuoteDatabase(t)
	if _, err := connection.DB.Exec("CREATE TABLE items (id INTEGER PRIMARY KEY, payload TEXT); INSERT INTO items VALUES (7, 'original'), (8, 'Xriginal'), (9, 'originaX')"); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []bool{false, true} {
		for _, mutateReturned := range []bool{false, true} {
			value := &snapshotMutableValue{Bytes: []byte("origina"), Nested: map[string][]byte{"suffix": []byte("l")}}
			query := database.Table("items").Where("payload", value)
			if raw {
				query = database.Table("items").WhereRaw("payload = ?", value)
			}
			if mutateReturned {
				predicate, err := query.Predicate()
				if err != nil {
					t.Fatal(err)
				}
				nodes, err := predicate.Nodes()
				if err != nil {
					t.Fatal(err)
				}
				value = nodes[0].Values[0].(*snapshotMutableValue)
			}
			value.Bytes[0] = 'X'
			value.Nested["suffix"][0] = 'X'
			_, args, err := query.BuildSelectSQL()
			if err != nil {
				t.Fatal(err)
			}
			args[0].(*snapshotMutableValue).Bytes[0] = 'Y'
			derived := query.Limit(1)
			for attempt := 0; attempt < 2; attempt++ {
				row, err := derived.Find()
				if err != nil || row["id"] != int64(7) {
					t.Fatalf("参数快照污染: raw=%t 返回值=%t row=%v err=%v", raw, mutateReturned, row, err)
				}
			}
		}
	}
}

// TestSQLConnectionRejectsInvalidSnapshot 验证直接 SQL 请求与事务入口没有绕过受控复制。
func TestSQLConnectionRejectsInvalidSnapshot(t *testing.T) {
	database, connection := newSQLiteQuoteDatabase(t)
	data := map[string]interface{}{}
	data["self"] = data
	if _, err := connection.Insert(t.Context(), NewInsertRequest("missing", data, "id", false)); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("直接插入未在执行 SQL 前拒绝: %v", err)
	}
	if _, err := connection.Update(t.Context(), NewUpdateRequest("missing", data, Predicate{}, "id")); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("直接更新未在执行 SQL 前拒绝: %v", err)
	}
	transaction, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.Table("missing").Insert(data); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("事务写入未拒绝: %v", err)
	}
	if _, err := transaction.Table("missing").Where("value", data).Find(); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("事务条件未拒绝: %v", err)
	}
}
