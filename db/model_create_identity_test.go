package db

import (
	"errors"
	"strings"
	"testing"
)

func TestModelCreateBindsExplicitPrimaryKeyWithoutAskingDriverForID(t *testing.T) {
	state := &insertIDFailureState{idErr: errors.New("驱动不返回生成 ID")}
	database, _ := newInsertIDFailureDB(t, state, true)
	model := NewModel(database, "business_records").PrimaryKey("code")
	record := &struct{ Code, Name string }{Code: " raw ", Name: "first"}
	if err := model.Setter("code", func(value any, _ map[string]any) any {
		return strings.TrimSpace(value.(string))
	}); err != nil {
		t.Fatal(err)
	}
	if err := model.Create(record); err != nil || record.Code != "raw" {
		t.Fatalf("显式主键规范化后应回填模型: %q, %v", record.Code, err)
	}
	if state.execs.Load() != 1 || state.ids.Load() != 0 {
		t.Fatalf("只应执行一次 INSERT，不读取生成 ID: writes=%d ids=%d", state.execs.Load(), state.ids.Load())
	}
}

func TestModelCreateReportsPartialWriteForIncompatibleFinalPrimaryKey(t *testing.T) {
	state := &insertIDFailureState{}
	database, _ := newInsertIDFailureDB(t, state, true)
	model := NewModel(database, "business_records").PrimaryKey("code")
	record := &struct {
		Code int64
		Name string
	}{Code: 7, Name: "first"}
	if err := model.Setter("code", func(any, map[string]any) any { return "external-code" }); err != nil {
		t.Fatal(err)
	}
	var partial *PartialWriteError
	err := model.Create(record)
	if !errors.Is(err, ErrPartialWrite) || !errors.Is(err, ErrInvalidModel) ||
		!errors.As(err, &partial) || partial.Result.Affected != 1 || partial.Result.Data["code"] != "external-code" {
		t.Fatalf("写入后主键类型不兼容应返回含最终数据的部分写入错误: %v", err)
	}
	if record.Code != 7 || state.execs.Load() != 1 || state.ids.Load() != 0 {
		t.Fatal("回填失败后应保留原主键，且只执行一次 INSERT")
	}
}

func TestModelCreateAndUpdateUseFinalExplicitKeyOnSQLite(t *testing.T) {
	database, raw := newModelScanSQLite(t)
	if _, err := raw.Exec(`CREATE TABLE business_records (code TEXT PRIMARY KEY, name TEXT NOT NULL);
		INSERT INTO business_records(code, name) VALUES ('original', 'unrelated');`); err != nil {
		t.Fatal(err)
	}
	model := NewModel(database, "business_records").PrimaryKey("code")
	if err := model.Setter("code", func(value any, _ map[string]any) any {
		return strings.ToUpper(value.(string))
	}); err != nil {
		t.Fatal(err)
	}
	if err := model.On(ModelBeforeInsert, func(data map[string]any) bool {
		data["code"] = "business/" + data["code"].(string)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	record := &struct{ Code, Name string }{Code: "original", Name: "first"}
	if err := model.Create(record); err != nil || record.Code != "business/ORIGINAL" {
		t.Fatalf("应回填插入事件后的业务主键: %q, %v", record.Code, err)
	}
	record.Name = "updated"
	if err := model.Update(record); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"original": "unrelated", "business/ORIGINAL": "updated"} {
		var got string
		if err := raw.QueryRow("SELECT name FROM business_records WHERE code = ?", key).Scan(&got); err != nil || got != want {
			t.Errorf("主键 %q 对应名称为 %q，期望 %q: %v", key, got, want, err)
		}
	}
}
