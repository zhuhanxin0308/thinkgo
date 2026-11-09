//go:build cgo

package db

import (
	"errors"
	"reflect"
	"testing"
)

// TestColumnSameFieldSQLite 覆盖直接扫描、行流、事务和模型入口的同字段投影。
func TestColumnSameFieldSQLite(t *testing.T) {
	for _, mode := range []string{"普通", "高级", "事务", "模型", "限定字段"} {
		t.Run(mode, func(t *testing.T) {
			database, connection := newSQLiteQuoteDatabase(t)
			if _, err := connection.DB.Exec("CREATE TABLE items (id INTEGER PRIMARY KEY, payload TEXT); INSERT INTO items VALUES (7, 'a'), (8, 'b')"); err != nil {
				t.Fatal(err)
			}
			query := database.Table("items").Field("payload").Where("id", 7)
			field := "id"
			if mode == "高级" {
				query = query.Distinct()
			} else if mode == "事务" {
				transaction, err := database.BeginTx(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer transaction.Rollback()
				query = transaction.Table("items").Field("payload").Where("id", 7)
			} else if mode == "限定字段" {
				field = "items.id"
			}
			var result interface{}
			var err error
			if mode == "模型" {
				result, err = NewModel(database, "items").newModelQuery().Where("id", 7).Column(field, field)
			} else {
				result, err = query.Column(field, field)
			}
			if err != nil || !reflect.DeepEqual(result, map[string]interface{}{"7": int64(7)}) {
				t.Fatalf("同字段列查询失败: result=%v err=%v", result, err)
			}
			if query.fields != "payload" {
				t.Fatal("Column 污染了原始查询的投影")
			}
		})
	}
}

// TestColumnSameFieldKeepsValidation 验证去重投影不会放宽重复键、NULL 键和重复结果列限制。
func TestColumnSameFieldKeepsValidation(t *testing.T) {
	database, connection := newSQLiteQuoteDatabase(t)
	if _, err := connection.DB.Exec("CREATE TABLE items (id INTEGER, payload TEXT); INSERT INTO items VALUES (7, 'a'), (8, 'a'), (9, NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Table("items").Where("id", "<", 9).Column("payload", "payload"); !errors.Is(err, ErrInvalidDatabaseRow) {
		t.Fatalf("重复键未拒绝: %v", err)
	}
	if _, err := database.Table("items").Where("id", 9).Column("payload", "payload"); !errors.Is(err, ErrInvalidDatabaseRow) {
		t.Fatalf("NULL 键未拒绝: %v", err)
	}
	result, err := database.Table("items").Where("id", 7).Column("payload", "id")
	if err != nil || !reflect.DeepEqual(result, map[string]interface{}{"7": "a"}) {
		t.Fatalf("不同字段回归: %v %v", result, err)
	}
	result, err = database.Table("items").Where("id", 7).Column("id")
	if err != nil || !reflect.DeepEqual(result, []interface{}{int64(7)}) {
		t.Fatalf("无键列表回归: %v %v", result, err)
	}
	result, err = database.Table("items").Where("id", 0).Column("id", "id")
	if err != nil || !reflect.DeepEqual(result, map[string]interface{}{}) {
		t.Fatalf("空结果回归: %v %v", result, err)
	}
	if _, err := database.Table("items").Field("id, id").Select(); !errors.Is(err, ErrInvalidDatabaseRow) {
		t.Fatalf("不明确的重复结果列不应放行: %v", err)
	}
}
