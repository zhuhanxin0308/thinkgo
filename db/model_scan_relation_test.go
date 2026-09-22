package db

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type modelScanProfileDTO struct {
	ID  int64
	Bio string
}

type modelScanPostDTO struct {
	ID    int64
	Title string
}

type modelScanNameDTO struct {
	ID   int64
	Name string
}

type modelScanRelationsRecord struct {
	*Model
	ID        int64
	Name      string
	CompanyID int64
	Profile   *modelScanProfileDTO `thinkgo:"profile,readonly"`
	Posts     []*modelScanPostDTO  `thinkgo:"posts,readonly"`
	Company   modelScanNameDTO     `thinkgo:"company,readonly"`
	Roles     []modelScanNameDTO   `thinkgo:"roles,readonly"`
}

// TestModelScanSQLiteEagerProjections 验证四种真实关联能映射为 DTO，且保存只触及父表。
func TestModelScanSQLiteEagerProjections(t *testing.T) {
	database, raw := newModelScanSQLite(t)
	statements := []string{
		"CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT NOT NULL, company_id INTEGER NOT NULL)",
		"CREATE TABLE profiles (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL, bio TEXT NOT NULL)",
		"CREATE TABLE posts (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL, title TEXT NOT NULL)",
		"CREATE TABLE companies (id INTEGER PRIMARY KEY, name TEXT NOT NULL)",
		"CREATE TABLE roles (id INTEGER PRIMARY KEY, name TEXT NOT NULL)",
		"CREATE TABLE user_role (user_id INTEGER NOT NULL, role_id INTEGER NOT NULL)",
		"INSERT INTO users VALUES (1, '原名', 10), (2, '无关联', 10)",
		"INSERT INTO profiles VALUES (20, 1, '简介')",
		"INSERT INTO posts VALUES (30, 1, '文章一'), (31, 1, '文章二')",
		"INSERT INTO companies VALUES (10, '公司')",
		"INSERT INTO roles VALUES (40, '管理员'), (41, '编辑')",
		"INSERT INTO user_role VALUES (1, 40), (1, 41)",
	}
	for _, statement := range statements {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	users := NewModel(database, "users")
	for _, err := range []error{
		users.DefineHasOne("profile", NewModel(database, "profiles"), "user_id", "id"),
		users.DefineHasMany("posts", NewModel(database, "posts"), "user_id", "id"),
		users.DefineBelongsTo("company", NewModel(database, "companies"), "company_id", "id"),
		users.DefineBelongsToMany("roles", NewModel(database, "roles"), "user_role", "user_id", "role_id", "id"),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	var records []*modelScanRelationsRecord
	if err := users.With("profile", "posts", "company", "roles").Order("id ASC").Select(&records); err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].Profile == nil || records[0].Profile.Bio != "简介" || len(records[0].Posts) != 2 || records[0].Posts[1].Title != "文章二" || records[0].Company.Name != "公司" || len(records[0].Roles) != 2 || records[0].Roles[0].Name != "管理员" {
		t.Fatalf("关联 DTO 内容错误: %#v", records)
	}
	if records[1].Profile != nil || records[1].Posts == nil || len(records[1].Posts) != 0 || records[1].Roles == nil || len(records[1].Roles) != 0 {
		t.Fatalf("空关联应映射为 nil 单关系和非 nil 空数组: %#v", records[1])
	}
	records[0].Name = "已修改"
	records[0].Profile.Bio = "不应写回"
	if err := records[0].Save(); err != nil {
		t.Fatalf("父记录保存不能把关联当作列: %v", err)
	}
	if err := records[0].SaveFields("profile"); !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("显式保存只读列应拒绝: %v", err)
	}
	var name, bio string
	if err := raw.QueryRow("SELECT name FROM users WHERE id = 1").Scan(&name); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow("SELECT bio FROM profiles WHERE id = 20").Scan(&bio); err != nil {
		t.Fatal(err)
	}
	if name != "已修改" || bio != "简介" {
		t.Fatalf("父子写入边界错误: name=%q bio=%q", name, bio)
	}
	created := modelScanRelationsRecord{Name: "新用户", CompanyID: 10, Profile: &modelScanProfileDTO{Bio: "只读"}}
	if err := users.Create(&created); err != nil || created.ID == 0 {
		t.Fatalf("创建应忽略只读投影: id=%d err=%v", created.ID, err)
	}
}

// TestModelScanReadonlyProjectionBoundaries 验证嵌套投影、类型失败和原子提交。
func TestModelScanReadonlyProjectionBoundaries(t *testing.T) {
	type leaf struct {
		Score int8
	}
	type branch struct {
		*ModelScanEmbedded
		Leaf     *leaf
		Children []*leaf
	}
	type targetType struct {
		ID     int64
		Detail *branch `thinkgo:"detail,readonly"`
	}
	original := &branch{ModelScanEmbedded: &ModelScanEmbedded{ID: 8, Name: "原值"}, Leaf: &leaf{Score: 9}, Children: []*leaf{{Score: 10}}}
	target := targetType{ID: 99, Detail: original}
	row := map[string]any{"id": int64(1), "detail": map[string]any{
		"id": int64(2), "display_name": "嵌套", "leaf": map[string]any{"score": int64(3)},
		"children": []map[string]any{{"score": int64(4)}, {"score": int64(128)}},
	}}
	if _, err := newModelScanQuery(row).Find(&target); !errors.Is(err, ErrInvalidDatabaseRow) || target.ID != 99 || target.Detail != original || original.Leaf.Score != 9 {
		t.Fatalf("后续关联元素失败污染了原目标: target=%#v err=%v", target, err)
	}
	row["detail"].(map[string]any)["children"] = []map[string]any{{"score": int64(4)}}
	if found, err := newModelScanQuery(row).Find(&target); err != nil || !found || target.Detail.ID != 2 || target.Detail.Name != "嵌套" || target.Detail.Leaf.Score != 3 || target.Detail.Children[0].Score != 4 || original.Name != "原值" {
		t.Fatalf("递归只读 DTO 映射失败: target=%#v err=%v", target, err)
	}
	for _, source := range []any{nil, map[string]any(nil)} {
		if _, err := newModelScanQuery(map[string]any{"id": int64(1), "detail": source}).Find(&target); err != nil || target.Detail != nil {
			t.Fatalf("不存在的单关系应清空旧指针: target=%#v err=%v", target, err)
		}
	}
	for _, source := range []any{int64(1), []map[string]any{}, map[string]any{"unknown": true}} {
		if _, err := newModelScanQuery(map[string]any{"id": int64(1), "detail": source}).Find(&target); !errors.Is(err, ErrInvalidDatabaseRow) {
			t.Fatalf("非法关联形态应拒绝: source=%#v err=%v", source, err)
		}
	}
	strict := struct{ Detail leaf }{}
	if _, err := newModelScanQuery(map[string]any{"detail": map[string]any{"score": int64(1)}}).Find(&strict); !errors.Is(err, ErrInvalidDatabaseRow) {
		t.Fatalf("普通字段不能隐式启用关联转换: %v", err)
	}
	badRecord := struct {
		ID      int64
		Profile *modelScanRecord `thinkgo:"profile,readonly"`
	}{ID: 9}
	if _, err := newModelScanQuery(map[string]any{"id": int64(1)}).Find(&badRecord); !errors.Is(err, ErrInvalidModel) || badRecord.ID != 9 {
		t.Fatalf("未加载的关联类型也不能嵌入 Model: target=%#v err=%v", badRecord, err)
	}
}

// TestModelScanReadonlyCancellation 验证关联内 Scanner 取消后不会发布父记录。
func TestModelScanReadonlyCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	target := struct {
		ID     int64
		Detail *struct{ Value modelScanCancelScanner } `thinkgo:"detail,readonly"`
	}{ID: 9}
	_, err := newModelScanQuery(map[string]any{"id": int64(1), "detail": map[string]any{"value": context.CancelFunc(cancel)}}).WithContext(ctx).Find(&target)
	if !errors.Is(err, context.Canceled) || target.ID != 9 || target.Detail != nil {
		t.Fatalf("关联取消没有保持原子性: target=%#v err=%v", target, err)
	}
	// 递归 DTO 类型允许有限层级数据，但循环数据必须明确拒绝。
	type node struct {
		ID   int64
		Next *node
	}
	row := map[string]any{"id": int64(1)}
	row["next"] = row
	value := reflect.New(reflect.TypeFor[node]()).Elem()
	if err := assignModelScanProjection(context.Background(), value, row, make(map[uintptr]bool)); err == nil {
		t.Fatal("循环关联数据没有被拒绝")
	}
}
