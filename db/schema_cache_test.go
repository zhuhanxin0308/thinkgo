package db

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestSchemaCacheLoadsIntoFreshConnection 验证连接重新创建后可以消费字段结构缓存，无需再查询系统表。
func TestSchemaCacheLoadsIntoFreshConnection(t *testing.T) {
	connection, recorder := newRecordingHardeningSQLConnection(t)
	database := NewDB(connection)
	cache := schemaCacheEnvelope{Version: schemaCacheVersion, Identity: "tenant-a", Dialect: connection.Builder.DialectName(), Tables: []TableSchema{{Table: "users", Columns: []SchemaColumn{{Name: "id", DataType: "INTEGER", PrimaryKey: true}, {Name: "name", DataType: "TEXT", Nullable: true}}}}}
	content, err := json.Marshal(cache)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.LoadSchemaCache(content, "tenant-a"); err != nil {
		t.Fatal(err)
	}
	info, err := database.GetSchemaInfo(context.Background(), "users", false)
	if err != nil || len(info.Columns) != 2 || !info.Columns[0].PrimaryKey {
		t.Fatalf("结构缓存读取错误: %#v %v", info, err)
	}
	if recorder.recordedQuery() != "" {
		t.Fatalf("命中结构缓存后仍访问系统表: %s", recorder.recordedQuery())
	}
	info.Columns[0].Name = "changed"
	again, err := database.GetSchemaInfo(context.Background(), "users", false)
	if err != nil || again.Columns[0].Name != "id" {
		t.Fatalf("调用方修改污染了共享缓存: %#v %v", again, err)
	}
	if fields := connection.cachedSchemaFields("users"); fields != "id,name" {
		t.Fatalf("查询没有可消费的结构字段: %s", fields)
	}
	if err := database.LoadSchemaCache(content, "tenant-b"); err == nil {
		t.Fatal("不同数据库身份共用了缓存")
	}
}

// TestSchemaCatalogQueriesRejectUnsafeIdentifiers 验证各 SQL 方言使用受检标识符或绑定参数。
func TestSchemaCatalogQueriesRejectUnsafeIdentifiers(t *testing.T) {
	for _, dialect := range []string{"mysql", "postgres", "sqlite", "sqlserver", "oracle"} {
		query, _, err := schemaCatalogQuery(dialect, "users")
		if err != nil || query == "" {
			t.Fatalf("方言 %s 缺少真实结构查询: %v", dialect, err)
		}
		for _, table := range []string{"", "users;DROP TABLE users", "../users", "a.b.c", "users--"} {
			if _, _, err := schemaCatalogQuery(dialect, table); err == nil {
				t.Fatalf("方言 %s 接受了非法表名 %q", dialect, table)
			}
		}
	}
	if _, _, err := schemaCatalogQuery("mongo", "users"); err == nil {
		t.Fatal("非 SQL 驱动不能伪装成关系型结构优化")
	}
}

// TestSchemaCacheRejectsMalformedContent 验证损坏、重复列、越界表名和不匹配方言不会进入运行时。
func TestSchemaCacheRejectsMalformedContent(t *testing.T) {
	database := NewDB(newHardeningSQLConnection(t))
	for _, content := range []string{`null`, `{}`, `{"version":1,"identity":"x","dialect":"missing","tables":[]}`, strings.Repeat("x", MaxSchemaCacheBytes+1)} {
		if err := database.LoadSchemaCache([]byte(content), "x"); err == nil {
			t.Fatal("非法结构缓存被接受")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := database.GetSchemaInfo(ctx, "users", false); err == nil {
		t.Fatal("已取消的结构读取没有返回错误")
	}
}
