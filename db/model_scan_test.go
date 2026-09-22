package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"
)

type ModelScanEmbedded struct {
	ID   int64  `thinkgo:"id"`
	Name string `thinkgo:"display_name"`
}

type modelScanRecord struct {
	*Model
	*ModelScanEmbedded
	Note    *string
	Score   sql.NullInt64
	Payload []byte
	Created time.Time
	Ignored string `thinkgo:"-"`
}

type ModelScanNestedModel struct {
	*Model
}

// newModelScanQuery 用稳定的行结果隔离字段映射边界，真实驱动另有集成测试。
func newModelScanQuery(rows ...map[string]any) *ModelQuery {
	return NewModel(NewDB(&modelBusinessConnection{rows: rows}), "scan_records").newModelQuery()
}

// TestModelScanFindPublishesCompleteRecord 验证嵌入、标签、空值和字节所有权同时生效。
func TestModelScanFindPublishesCompleteRecord(t *testing.T) {
	instant := time.Date(2026, time.September, 6, 8, 30, 0, 0, time.UTC)
	query := newModelScanQuery(map[string]any{
		"id": int64(7), "display_name": []byte("新名称"), "note": nil,
		"score": int64(12), "payload": []byte{1, 2}, "created": instant,
		"unused_column": "投影中未声明的字段",
	})
	oldEmbedded := &ModelScanEmbedded{ID: 3, Name: "旧名称"}
	oldNote := "旧备注"
	target := modelScanRecord{ModelScanEmbedded: oldEmbedded, Note: &oldNote, Ignored: "保留"}
	found, err := query.Find(&target)
	if err != nil || !found {
		t.Fatalf("结构体查询失败: found=%v err=%v", found, err)
	}
	if target.ID != 7 || target.Name != "新名称" || target.Note != nil || target.Score != (sql.NullInt64{Int64: 12, Valid: true}) || !target.Created.Equal(instant) || target.Ignored != "保留" {
		t.Fatalf("字段映射结果错误: %#v", target)
	}
	if target.ModelScanEmbedded == oldEmbedded || oldEmbedded.ID != 3 || oldEmbedded.Name != "旧名称" {
		t.Fatalf("映射修改了原有嵌入指针: old=%#v new=%#v", oldEmbedded, target.ModelScanEmbedded)
	}
}

// TestModelScanProjectionAndMissingRows 验证部分投影保留已有字段，空结果不会伪装成记录。
func TestModelScanProjectionAndMissingRows(t *testing.T) {
	target := ModelScanEmbedded{ID: 4, Name: "保留"}
	found, err := newModelScanQuery(map[string]any{"id": int64(9)}).Find(&target)
	if err != nil || !found || target.ID != 9 || target.Name != "保留" {
		t.Fatalf("部分投影错误: found=%v target=%#v err=%v", found, target, err)
	}
	found, err = newModelScanQuery().Find(&target)
	if err != nil || found || target.ID != 9 || target.Name != "保留" {
		t.Fatalf("无记录时改变了目标: found=%v target=%#v err=%v", found, target, err)
	}
	rows := []ModelScanEmbedded{{ID: 99}}
	if err := newModelScanQuery().Select(&rows); err != nil || rows == nil || len(rows) != 0 {
		t.Fatalf("空查询必须产生空切片: rows=%#v err=%v", rows, err)
	}
}

// TestModelScanNestedBindingDoesNotMutateAliases 验证只投影外层字段时，嵌套模型绑定也会复制指针。
func TestModelScanNestedBindingDoesNotMutateAliases(t *testing.T) {
	query := newModelScanQuery(map[string]any{"id": int64(8)})
	oldModel := query.model
	original := &ModelScanNestedModel{Model: oldModel}
	target := struct {
		*ModelScanNestedModel
		ID int64
	}{ModelScanNestedModel: original, ID: 2}
	found, err := query.Find(&target)
	if err != nil || !found || target.ID != 8 || target.ModelScanNestedModel == original || original.Model != oldModel {
		t.Fatalf("记录绑定污染了原嵌入别名: target=%#v original=%#v err=%v", target, original, err)
	}
}

// TestModelScanRefreshRejectsReplacedHandle 验证旧模型句柄不能覆盖后来查询到的另一条记录。
func TestModelScanRefreshRejectsReplacedHandle(t *testing.T) {
	target := struct {
		*Model
		ID int64
	}{}
	if found, err := newModelScanQuery(map[string]any{"id": int64(1)}).Find(&target); err != nil || !found {
		t.Fatalf("第一次加载失败: %v", err)
	}
	previous := target.Model
	if found, err := newModelScanQuery(map[string]any{"id": int64(2)}).Find(&target); err != nil || !found {
		t.Fatalf("替换记录失败: %v", err)
	}
	current := target.Model
	if err := previous.Refresh(); !errors.Is(err, ErrModelRecordStale) || target.ID != 2 || target.Model != current {
		t.Fatalf("旧句柄 Refresh 覆盖了当前记录: id=%d err=%v", target.ID, err)
	}
}

// TestModelScanSelectCommitsAtomically 验证值切片和指针切片均在全部映射成功后发布。
func TestModelScanSelectCommitsAtomically(t *testing.T) {
	query := newModelScanQuery(map[string]any{"id": int64(1)}, map[string]any{"id": "错误整数"})
	original := ModelScanEmbedded{ID: 88, Name: "原值"}
	values := []ModelScanEmbedded{original}
	backing := values
	if err := query.Select(&values); !errors.Is(err, ErrInvalidDatabaseRow) || !reflect.DeepEqual(values, backing) || values[0] != original {
		t.Fatalf("失败查询部分修改了值切片: values=%#v err=%v", values, err)
	}
	pointers := []*ModelScanEmbedded{&original}
	if err := query.Select(&pointers); !errors.Is(err, ErrInvalidDatabaseRow) || len(pointers) != 1 || pointers[0] != &original || original.ID != 88 {
		t.Fatalf("失败查询部分修改了指针切片: rows=%#v err=%v", pointers, err)
	}
	query = newModelScanQuery(map[string]any{"id": int64(1)}, map[string]any{"id": int64(2)})
	if err := query.Select(&pointers); err != nil || len(pointers) != 2 || pointers[0] == pointers[1] || pointers[0].ID != 1 || pointers[1].ID != 2 {
		t.Fatalf("指针切片成功映射错误: rows=%#v err=%v", pointers, err)
	}
	if err := query.Select(&values); err != nil || len(values) != 2 || values[1].ID != 2 {
		t.Fatalf("值切片成功映射错误: rows=%#v err=%v", values, err)
	}
}

// TestModelScanRejectsRecordValueSlices 验证拥有保存状态的记录不能被切片扩容隐式复制。
func TestModelScanRejectsRecordValueSlices(t *testing.T) {
	values := []modelScanRecord{{Ignored: "原记录"}}
	if err := newModelScanQuery().Select(&values); !errors.Is(err, ErrInvalidModel) || len(values) != 1 || values[0].Ignored != "原记录" {
		t.Fatalf("接受了会搬移拥有者的记录值切片: values=%#v err=%v", values, err)
	}
	var pointers []*modelScanRecord
	if err := newModelScanQuery(map[string]any{"id": int64(7)}).Select(&pointers); err != nil || len(pointers) != 1 || pointers[0].Model == nil || pointers[0].ID != 7 {
		t.Fatalf("合法记录指针切片未正常绑定: rows=%#v err=%v", pointers, err)
	}
}

// TestModelScanRejectsInvalidDestinations 验证目标和字段声明先于数据库执行被校验。
func TestModelScanRejectsInvalidDestinations(t *testing.T) {
	var nilRecord *ModelScanEmbedded
	for _, target := range []any{nil, nilRecord, ModelScanEmbedded{}, new(int), new([]int), new(**ModelScanEmbedded)} {
		if _, err := newModelScanQuery().Find(target); !errors.Is(err, ErrInvalidModel) {
			t.Errorf("Find接受了非法目标 %T: %v", target, err)
		}
	}
	for _, target := range []any{nil, nilRecord, []ModelScanEmbedded{}, new(int), new([]int), new([]**ModelScanEmbedded)} {
		if err := newModelScanQuery().Select(target); !errors.Is(err, ErrInvalidModel) {
			t.Errorf("Select接受了非法目标 %T: %v", target, err)
		}
	}
	duplicate := struct {
		A int `thinkgo:"id"`
		B int `thinkgo:"id"`
	}{}
	if _, err := newModelScanQuery().Find(&duplicate); !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("未拒绝重复列映射: %v", err)
	}
	target := ModelScanEmbedded{ID: 3}
	if _, err := newModelScanQuery(map[string]any{"unknown": 1}).Find(&target); !errors.Is(err, ErrInvalidDatabaseRow) || target.ID != 3 {
		t.Fatalf("无匹配字段未失败关闭: target=%#v err=%v", target, err)
	}
	var nilQuery *ModelQuery
	if _, err := nilQuery.Find(&target); !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("空查询未失败关闭: %v", err)
	}
}

var errModelScanFixture = errors.New("扫描器测试错误")

type modelScanFailingScanner struct {
	Value string
}

func (scanner *modelScanFailingScanner) Scan(value any) error {
	scanner.Value = "已接收新值"
	return errModelScanFixture
}

type modelScanValue struct {
	value driver.Value
	err   error
}

func (value modelScanValue) Value() (driver.Value, error) {
	return value.value, value.err
}

type modelScanCancelScanner struct{}

func (*modelScanCancelScanner) Scan(source any) error {
	source.(context.CancelFunc)()
	return nil
}

type modelScanInvalidValuer struct {
	buffer []byte
}

func (value modelScanInvalidValuer) Value() (driver.Value, error) {
	return struct{ buffer []byte }{buffer: value.buffer}, nil
}

// TestModelScanBindingRejectsInvalidValuerSnapshots 验证非法 Valuer 不能把共享私有状态写入脏字段快照。
func TestModelScanBindingRejectsInvalidValuerSnapshots(t *testing.T) {
	target := struct {
		*Model
		ID    int64
		Value modelScanInvalidValuer
	}{ID: 99, Value: modelScanInvalidValuer{buffer: []byte("原值")}}
	_, err := newModelScanQuery(map[string]any{"id": int64(7)}).Find(&target)
	if !errors.Is(err, ErrInvalidModel) || target.ID != 99 || target.Model != nil {
		t.Fatalf("非法 Valuer 快照未被拒绝或已发布记录: target=%#v err=%v", target, err)
	}
}

// TestModelScanScannerFailurePreservesOriginal 验证自定义扫描器即使先写后报错也不污染目标。
func TestModelScanScannerFailurePreservesOriginal(t *testing.T) {
	scanner := &modelScanFailingScanner{Value: "原值"}
	target := struct {
		ID      int64
		Scanner *modelScanFailingScanner
	}{ID: 99, Scanner: scanner}
	_, err := newModelScanQuery(map[string]any{"id": int64(1), "scanner": "新值"}).Find(&target)
	if !errors.Is(err, errModelScanFixture) || !errors.Is(err, ErrInvalidDatabaseRow) || target.ID != 99 || target.Scanner != scanner || scanner.Value != "原值" {
		t.Fatalf("扫描器错误破坏了原记录或错误链: target=%#v scanner=%#v err=%v", target, scanner, err)
	}
}

// TestModelScanScannerCancellationAndBufferOwnership 验证扫描阶段取消和可变驱动值的所有权。
func TestModelScanScannerCancellationAndBufferOwnership(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	target := struct {
		ID      int64
		Scanner modelScanCancelScanner
	}{ID: 91}
	query := newModelScanQuery(map[string]any{"id": int64(1), "scanner": context.CancelFunc(cancel)}).WithContext(ctx)
	if _, err := query.Find(&target); !errors.Is(err, context.Canceled) || target.ID != 91 {
		t.Fatalf("扫描器期间取消仍发布了记录: target=%#v err=%v", target, err)
	}
	source := []byte{1, 2, 3}
	for _, value := range []any{new([]byte), new(any)} {
		destination := reflect.ValueOf(value).Elem()
		if err := assignModelScanValue(destination, source); err != nil {
			t.Fatal(err)
		}
		converted := destination.Interface().([]byte)
		converted[0] = 99
		if source[0] != 1 {
			t.Fatalf("%s 映射没有复制驱动缓冲区", destination.Type())
		}
	}
}

// TestModelScanConversionBoundaries 验证驱动标量和命名类型不会发生静默截断。
func TestModelScanConversionBoundaries(t *testing.T) {
	type namedInteger int16
	type namedBytes []byte
	type namedBoolean bool
	type namedText string
	instant := time.Date(2026, time.September, 6, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		source any
		want   any
		fails  bool
	}{
		{"integer_text", "32767", namedInteger(32767), false},
		{"integer_bytes", []byte("12"), int64(12), false},
		{"negative_unsigned", int64(-1), uint64(0), true},
		{"integer_overflow", int64(128), int8(0), true},
		{"unsigned_overflow", uint64(256), uint8(0), true},
		{"uint64_exact", "18446744073709551615", uint64(math.MaxUint64), false},
		{"fraction_to_integer", 1.5, int64(0), true},
		{"float_overflow", "1e40", float32(0), true},
		{"non_finite", math.Inf(1), float64(0), true},
		{"nan_text", "NaN", float64(0), true},
		{"boolean_integer", int64(1), true, false},
		{"invalid_boolean", int64(2), false, true},
		{"boolean_text", "false", false, false},
		{"boolean_named", namedBoolean(true), namedBoolean(true), false},
		{"boolean_named_text", namedText("true"), namedBoolean(true), false},
		{"string_integer", int64(12), "12", false},
		{"binary_named", "文本", namedBytes("文本"), false},
		{"native_time", instant, instant, false},
		{"native_time_text", instant, instant.Format(time.RFC3339Nano), false},
		{"integer_float", int64(15), float64(15), false},
		{"time_text_rejected", "2026-09-06", time.Time{}, true},
		{"null_string", nil, "", true},
		{"null_pointer", nil, (*string)(nil), false},
		{"null_bytes", nil, []byte(nil), false},
		{"null_scanner", nil, sql.NullString{}, false},
		{"scanner_value", "abc", sql.NullString{String: "abc", Valid: true}, false},
		{"valuer_source", modelScanValue{value: "42"}, int64(42), false},
		{"valuer_error", modelScanValue{err: errModelScanFixture}, "", true},
		{"valuer_invalid_result", modelScanValue{value: struct{}{}}, "", true},
		{"unsupported_type", "abc", struct{ X int }{}, true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			destination := reflect.New(reflect.TypeOf(test.want)).Elem()
			err := assignModelScanValue(destination, test.source)
			if test.fails {
				if err == nil {
					t.Fatalf("非法转换未报错: source=%#v target=%v", test.source, destination.Type())
				}
				return
			}
			if err != nil || !reflect.DeepEqual(destination.Interface(), test.want) {
				t.Fatalf("转换错误: actual=%#v want=%#v err=%v", destination.Interface(), test.want, err)
			}
		})
	}
}

// TestModelScanCancellationBeforePublication 验证 Getter 中发生的取消也阻止最终提交。
func TestModelScanCancellationBeforePublication(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	query := newModelScanQuery(map[string]any{"id": int64(7)}).WithContext(ctx)
	if err := query.model.Getter("id", func(value any, _ map[string]any) any {
		cancel()
		return value
	}); err != nil {
		t.Fatal(err)
	}
	target := ModelScanEmbedded{ID: 99}
	if _, err := query.Find(&target); !errors.Is(err, context.Canceled) || target.ID != 99 {
		t.Fatalf("Getter取消后仍提交了目标: target=%#v err=%v", target, err)
	}
	rows := []ModelScanEmbedded{{ID: 88}}
	if err := query.Select(&rows); !errors.Is(err, context.Canceled) || len(rows) != 1 || rows[0].ID != 88 {
		t.Fatalf("预先取消后仍提交了切片: rows=%#v err=%v", rows, err)
	}
}
