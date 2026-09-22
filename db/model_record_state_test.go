package db

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type RecordEmbeddedFields struct {
	ID   int64  `thinkgo:"id"`
	Name string `thinkgo:"name,omitempty"`
}

type recordStateUser struct {
	*Model
	*RecordEmbeddedFields
	Email   string `thinkgo:"email"`
	Payload []byte `thinkgo:"payload"`
}

func TestRecordMetadataEmbedsAndSnapshots(t *testing.T) {
	conn := &capturingConnection{}
	user := &recordStateUser{RecordEmbeddedFields: &RecordEmbeddedFields{ID: 7, Name: "before"}, Payload: []byte("a")}
	m, err := NewModelFor(context.Background(), NewDB(conn), user)
	if err != nil {
		t.Fatal(err)
	}
	if user.Model != m {
		t.Fatal("模型没有注入拥有者")
	}
	metadata, err := loadModelMetadata(reflect.TypeOf(*user))
	if err != nil || len(metadata.fields) != 4 {
		t.Fatalf("嵌入字段映射错误: %+v %v", metadata, err)
	}
	target := reflect.ValueOf(user).Elem()
	if err := m.newModelQuery().bindScannedRecord(target, target, map[string]any{"id": int64(7), "name": "before", "payload": []byte("a")}); err != nil {
		t.Fatal(err)
	}
	user.Name = ""
	user.Payload[0] = 'b'
	if err := user.Save(); err != nil {
		t.Fatal(err)
	}
	if conn.lastData["name"] != "" || string(conn.lastData["payload"].([]byte)) != "b" {
		t.Fatalf("丢失零值或可变字段修改: %#v", conn.lastData)
	}
	if _, exists := conn.lastData["email"]; exists {
		t.Fatal("写入了未加载字段")
	}
	conn.lastData = nil
	if err := user.Save(); err != nil || conn.lastData != nil {
		t.Fatalf("无变更保存不应执行SQL: %v %#v", err, conn.lastData)
	}
	user.ID = 8
	if err := user.Save(); !errors.Is(err, ErrModelIdentityChanged) {
		t.Fatalf("主键变更必须拒绝: %v", err)
	}
}

func TestModelBindingRejectsInvalidOwnersAndContext(t *testing.T) {
	database := NewDB(&capturingConnection{})
	for _, owner := range []any{nil, recordStateUser{}, &struct{ ID int64 }{}} {
		if _, err := NewModelFor(context.Background(), database, owner); !errors.Is(err, ErrInvalidModel) {
			t.Fatalf("接受非法owner %T: %v", owner, err)
		}
	}
	//lint:ignore SA1012 此处专门验证空上下文被拒绝，不能用有效上下文替代错误输入。
	if _, err := NewModelFor(nil, database, &recordStateUser{}); !errors.Is(err, ErrInvalidDatabaseContext) {
		t.Fatalf("接受nil上下文: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	user := &recordStateUser{RecordEmbeddedFields: &RecordEmbeddedFields{Name: "new"}}
	if _, err := NewModelFor(ctx, database, user); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := user.On(ModelBeforeInsert, func(data map[string]interface{}) bool { called = true; return true }); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := user.Save(); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消请求仍写入: %v", err)
	}
	if called {
		t.Fatal("取消请求仍执行写回调")
	}
}
