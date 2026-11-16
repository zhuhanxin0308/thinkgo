package db

import (
	"errors"
	"testing"
)

func TestModelFillableRejectsMapInputBeforeHooksAndWrites(t *testing.T) {
	connection := &modelPrimaryKeyConnection{insertID: int64(3)}
	model := NewModel(NewDB(connection), "users")
	if err := model.SetFillable("name"); err != nil {
		t.Fatal(err)
	}
	setterCalls := 0
	if err := model.Setter("name", func(value any, _ map[string]any) any {
		setterCalls++
		return value
	}); err != nil {
		t.Fatal(err)
	}
	for name, write := range map[string]func(map[string]any) error{
		"插入":        func(data map[string]any) error { _, err := model.Insert(data); return err },
		"插入并回传主键":   func(data map[string]any) error { _, err := model.InsertGetId(data); return err },
		"更新":        func(data map[string]any) error { _, err := model.Where("id", 1).Update(data); return err },
		"模型 map 更新": func(data map[string]any) error { _, err := model.UpdateMap(data); return err },
		"批量插入": func(data map[string]any) error {
			_, err := model.Query().InsertAll([]map[string]any{{"name": "first"}, data})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			for _, field := range []string{"is_admin", "users.name", "NAME", "`name`"} {
				if err := write(map[string]any{"name": "Ada", field: true}); !errors.Is(err, ErrModelFieldNotFillable) {
					t.Fatalf("写入应拒绝未授权字段 %q: %v", field, err)
				}
			}
		})
	}
	if setterCalls != 0 || connection.insertCalls.Load() != 0 || connection.updateCalls.Load() != 0 {
		t.Fatal("未经授权的数据到达了业务修改器或数据库")
	}
	data := map[string]any{"name": "Ada"}
	if count, err := model.Insert(data); err != nil || count != 1 {
		t.Fatalf("白名单中的字段无法插入: %d %v", count, err)
	}
	if count, err := model.Where("id", 1).Update(data); err != nil || count != 1 {
		t.Fatalf("白名单中的字段无法更新: %d %v", count, err)
	}
	if setterCalls != 2 {
		t.Fatalf("合法请求未经过原有修改器: %d", setterCalls)
	}
}

func TestModelFillableEmptyInvalidAndDerivedPolicies(t *testing.T) {
	connection := &modelPrimaryKeyConnection{
		insertID: int64(9), capabilities: DriverCapabilities{InsertIDKinds: []InsertIDKind{InsertIDInteger}},
	}
	model := NewModel(NewDB(connection), "users")
	if _, err := model.Insert(map[string]any{"role": "operator"}); err != nil {
		t.Fatalf("未配置白名单的服务端 map 写入失败: %v", err)
	}
	if err := model.SetFillable("name"); err != nil {
		t.Fatal(err)
	}
	derived := model.WithContext(t.Context())
	for _, fields := range [][]string{{"name", "name"}, {"users.name"}, {""}} {
		if err := model.SetFillable(fields...); !errors.Is(err, ErrInvalidModel) {
			t.Fatalf("非法白名单配置 %#v 未被拒绝: %v", fields, err)
		}
	}
	if _, err := model.Insert(map[string]any{"name": "Ada"}); err != nil {
		t.Fatalf("非法配置覆盖了现有白名单: %v", err)
	}
	if err := model.SetFillable(); err != nil {
		t.Fatal(err)
	}
	if _, err := model.Insert(map[string]any{"name": "Ada"}); !errors.Is(err, ErrModelFieldNotFillable) {
		t.Fatalf("显式空白名单允许了 map 写入: %v", err)
	}
	if _, err := derived.Insert(map[string]any{"name": "Ada"}); err != nil {
		t.Fatalf("派生模型的白名单被原模型修改: %v", err)
	}
	if _, err := derived.Insert(map[string]any{"role": "admin"}); !errors.Is(err, ErrModelFieldNotFillable) {
		t.Fatalf("派生模型丢失了原白名单: %v", err)
	}
	typed := &modelSaveUser{Name: "Ada"}
	if err := model.Create(typed); err != nil || typed.ID != 9 {
		t.Fatalf("类型化写入失败: %#v %v", typed, err)
	}
	if err := model.Update(typed); err != nil {
		t.Fatalf("类型化更新失败: %v", err)
	}
}

func TestModelFillableBatchValidatesEveryRowBeforeSQL(t *testing.T) {
	connection, recorder := newRecordingHardeningSQLConnection(t)
	model := NewModel(NewDB(connection), "users")
	if err := model.SetFillable("name"); err != nil {
		t.Fatal(err)
	}
	rows := []map[string]any{{"name": "Ada"}, {"name": "Grace", "role": "admin"}}
	if _, err := model.Query().InsertAll(rows); !errors.Is(err, ErrModelFieldNotFillable) || recorder.recordedQuery() != "" {
		t.Fatalf("非法批量写入发出了 SQL: %q %v", recorder.recordedQuery(), err)
	}
	delete(rows[1], "role")
	if count, err := model.Query().InsertAll(rows); err != nil || count != 2 {
		t.Fatalf("合法批量写入失败: %d %v", count, err)
	}
}
