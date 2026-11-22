package db

import (
	"bytes"
	"database/sql/driver"
	"reflect"
	"testing"
)

const modelBlobBenchmarkSize = 1 << 20

type modelBlobBytes []byte
type modelBlobOctet uint8
type modelBlobDTO struct{ Payload modelBlobBytes }
type modelBlobRecord struct {
	*Model
	ID      int64
	Payload modelBlobBytes
}

type modelBlobValuer []byte

func (v modelBlobValuer) Value() (driver.Value, error) { return int64(len(v)), nil }

func TestModelBlobSnapshotAllocationAndOwnership(t *testing.T) {
	const sampleSize = 4096
	payload := make(modelBlobBytes, sampleSize)
	field := reflect.ValueOf(&payload).Elem()
	var original any
	allocations := testing.AllocsPerRun(10, func() {
		var err error
		original, err = snapshotRecordValue(field, make(map[recordSnapshotVisit]bool))
		if err != nil {
			t.Fatal(err)
		}
	})
	// 字节数增加只应扩大一个连续缓冲区，容许反射和接口持有固定数量的辅助对象。
	const maxSnapshotAllocations = 10
	if allocations > maxSnapshotAllocations {
		t.Fatalf("快照 %d 字节分配 %.0f 次", len(payload), allocations)
	}
	payload[len(payload)-1]++
	changed, err := snapshotRecordValue(field, make(map[recordSnapshotVisit]bool))
	if err != nil || reflect.DeepEqual(original, changed) {
		t.Fatalf("原地修改没有与原快照隔离: %v", err)
	}
	nilSnapshot, err := snapshotRecordValue(reflect.ValueOf(modelBlobBytes(nil)), nil)
	if err != nil || nilSnapshot != nil {
		t.Fatalf("nil 快照错误: %#v %v", nilSnapshot, err)
	}
	emptySnapshot, err := snapshotRecordValue(reflect.ValueOf(modelBlobBytes{}), make(map[recordSnapshotVisit]bool))
	if err != nil || reflect.DeepEqual(nilSnapshot, emptySnapshot) {
		t.Fatalf("空快照与 nil 混淆: %#v %v", emptySnapshot, err)
	}
	valued, err := snapshotRecordValue(reflect.ValueOf(modelBlobValuer("ab")), make(map[recordSnapshotVisit]bool))
	if err != nil || valued != int64(2) {
		t.Fatalf("具名字节字段未遵守 Valuer: %#v %v", valued, err)
	}
}

func TestModelBlobScanCopiesOnce(t *testing.T) {
	// 两端都使用具名元素，验证批量复制无需源和目标切片类型相同。
	var target []modelBlobOctet
	field := reflect.ValueOf(&target).Elem()
	raw := make(modelBlobBytes, 4096)
	raw[0] = 0xff
	var source any = raw
	allocations := testing.AllocsPerRun(10, func() {
		if err := convertModelScanValue(field, source); err != nil {
			t.Fatal(err)
		}
	})
	const maxScanAllocations = 1
	if allocations > maxScanAllocations {
		t.Fatalf("复制一个 BLOB 缓冲区分配 %.0f 次", allocations)
	}
	raw[0] = 0
	if target[0] != 0xff || len(target) != len(raw) || cap(target) != len(raw) {
		t.Fatal("扫描结果没有保留独立且大小正确的缓冲区")
	}
}

func TestModelBlobFindAndSelect(t *testing.T) {
	raw := bytes.Repeat([]byte{0, 0xff}, modelBlobBenchmarkSize/2)
	query := newModelScanQuery(
		map[string]any{"payload": raw}, map[string]any{"payload": modelBlobBytes{1}},
		map[string]any{"payload": nil}, map[string]any{"payload": []byte{}}, map[string]any{"payload": []byte(nil)},
	)
	var first modelBlobDTO
	var rows []modelBlobDTO
	if found, err := query.Find(&first); err != nil || !found || !bytes.Equal(first.Payload, raw) {
		t.Fatalf("Find 未完整扫描 BLOB: found=%v, err=%v", found, err)
	}
	if err := query.Select(&rows); err != nil || len(rows) != 5 {
		t.Fatalf("Select 返回 %d 行: %v", len(rows), err)
	}
	if !bytes.Equal(rows[0].Payload, raw) || !bytes.Equal(rows[1].Payload, []byte{1}) ||
		rows[2].Payload != nil || rows[3].Payload == nil || rows[4].Payload == nil {
		t.Fatal("二进制数据、SQL NULL 或长度为零的二进制源被错误映射")
	}
	raw[0]++
	rows[0].Payload[1] = 0
	if first.Payload[0] != 0 || first.Payload[1] != 0xff || rows[0].Payload[0] != 0 {
		t.Fatal("两次查询不应共享驱动或彼此的二进制缓冲区")
	}
}

func TestModelBlobSaveTracksOnlyTheChangedRecord(t *testing.T) {
	source := make([]byte, modelBlobBenchmarkSize)
	connection := &modelBusinessConnection{
		rows: []map[string]any{
			{"id": int64(1), "payload": source}, {"id": int64(2), "payload": source},
		},
		updateCount: 1,
	}
	query := NewModel(NewDB(connection), "blobs").newModelQuery()
	var records []*modelBlobRecord
	if err := query.Select(&records); err != nil || len(records) != 2 {
		t.Fatalf("绑定记录失败: %v", err)
	}
	last := len(source) - 1
	records[0].Payload[last] = 0xff
	if err := records[1].Save(); err != nil || connection.updateData != nil || source[last] != 0 {
		t.Fatalf("修改数据影响了其他记录或驱动缓冲区: %v", err)
	}
	if err := records[0].Save(); err != nil {
		t.Fatal(err)
	}
	written, ok := connection.updateData["payload"].(modelBlobBytes)
	if !ok || !bytes.Equal(written, records[0].Payload) || !reflect.DeepEqual(connection.lastWhereArgs, []any{int64(1)}) {
		t.Fatal("保存的二进制字段或记录身份错误")
	}
	connection.updateData = nil
	for range 2 {
		if err := records[0].Save(); err != nil || connection.updateData != nil {
			t.Fatalf("连续无变更保存触发了写入: %v", err)
		}
	}
}

func BenchmarkModelBlob(b *testing.B) {
	query := newModelScanQuery(map[string]any{"id": int64(7), "payload": make([]byte, modelBlobBenchmarkSize)})
	b.Run("DTOFind", func(b *testing.B) {
		for b.Loop() {
			var dto modelBlobDTO
			if found, err := query.Find(&dto); err != nil || !found {
				b.Fatalf("Find 失败: %v", err)
			}
		}
	})
	b.Run("RecordFind", func(b *testing.B) {
		for b.Loop() {
			var record modelBlobRecord
			if found, err := query.Find(&record); err != nil || !found {
				b.Fatalf("记录绑定失败: %v", err)
			}
		}
	})
	b.Run("SaveUnchanged", func(b *testing.B) {
		var record modelBlobRecord
		if found, err := query.Find(&record); err != nil || !found {
			b.Fatalf("加载记录失败: %v", err)
		}
		for b.Loop() {
			if err := record.Save(); err != nil {
				b.Fatal(err)
			}
		}
	})
}
