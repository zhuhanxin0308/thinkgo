package db

import (
	"errors"
	"strconv"
	"testing"
	"time"
)

func TestModelRecordRowsAvoidCopiesWithoutTransformations(t *testing.T) {
	query := newModelScanQuery()
	const rowsPerPage = 64
	rows := make([]map[string]any, rowsPerPage)
	for index := range rows {
		rows[index] = map[string]any{
			"id": int64(index + 1), "name": "商品", "live": true, "price": 2.5,
			"payload": []byte{1, 2}, "created": time.Unix(0, 0), "note": nil,
		}
	}
	allocations := testing.AllocsPerRun(20, func() {
		mapped, raw, err := query.prepareRecordRows(rows)
		if err != nil || len(mapped) != rowsPerPage || len(raw) != rowsPerPage {
			t.Fatalf("准备模型行失败: %v", err)
		}
	})
	t.Logf("准备 %d 条无变换模型行分配 %.0f 次", rowsPerPage, allocations)
	if allocations != 0 {
		t.Fatalf("无变换的驱动标量行应直接交给模型扫描，实际分配 %.0f 次", allocations)
	}
}

func TestModelRecordRowsKeepOriginalValuesBeforeGetters(t *testing.T) {
	query := newModelScanQuery()
	if err := query.model.Getter("payload", func(value any, row map[string]any) any {
		row["id"] = int64(99)
		value.([]byte)[0] = 9
		return value
	}); err != nil {
		t.Fatal(err)
	}
	roots := []map[string]any{{"id": int64(7), "payload": []byte{1, 2}}}
	mapped, raw, err := query.prepareRecordRows(roots)
	if err != nil {
		t.Fatal(err)
	}
	if mapped[0]["id"] != int64(99) || mapped[0]["payload"].([]byte)[0] != 9 ||
		raw[0]["id"] != int64(7) || raw[0]["payload"].([]byte)[0] != 1 {
		t.Fatalf("获取器快照边界错误，模型行 %#v，数据库原值 %#v", mapped, raw)
	}
}

func TestModelRecordRowsValidateCustomDriverValues(t *testing.T) {
	query := newModelScanQuery()
	invalid := []map[string]any{{"id": int64(1)}, {"id": int64(2), "private": make(chan int)}}
	if _, _, err := query.prepareRecordRows(invalid); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("不能复制的驱动值应使用现有快照校验，实际为 %v", err)
	}
}

func TestModelRecordRowsBudgetIncludesMapKeys(t *testing.T) {
	// 行占一个节点，每个非空字段包含键、接口和具体值三个节点。
	const fields = (maxDatabaseValueNodes-1)/3 + 1
	row := make(map[string]any, fields)
	for column := range fields {
		row[strconv.Itoa(column)] = int64(1)
	}
	if reusableModelRow(row) {
		t.Fatalf("%d 个标准字段超过原始映射的快照预算，必须走完整校验", fields)
	}
}
