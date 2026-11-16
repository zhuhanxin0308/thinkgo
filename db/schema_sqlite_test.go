package db

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestSchemaCacheSQLiteLifecycle 验证真实系统表读取、复合主键、跨连接缓存和实时星号投影。
func TestSchemaCacheSQLiteLifecycle(t *testing.T) {
	database, raw := newModelScanSQLite(t)
	if _, err := raw.Exec(`CREATE TABLE schema_items (tenant INTEGER NOT NULL, code TEXT NOT NULL, note TEXT DEFAULT 'hello', PRIMARY KEY (tenant, code))`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	schema, err := database.GetSchemaInfo(ctx, "schema_items", true)
	if err != nil || len(schema.Columns) != 3 || !schema.Columns[0].PrimaryKey || !schema.Columns[1].PrimaryKey || schema.Columns[0].Nullable || !schema.Columns[2].Nullable {
		t.Fatalf("真实表结构不完整: %+v %v", schema, err)
	}
	if schema.Columns[2].DefaultValue == nil || *schema.Columns[2].DefaultValue != "'hello'" {
		t.Fatalf("字段默认值错误: %+v", schema.Columns[2])
	}
	for _, namespace := range []string{"", "main"} {
		tables, err := database.SchemaTables(ctx, namespace)
		if err != nil || len(tables) != 2 {
			t.Fatalf("枚举真实表失败: %v %v", tables, err)
		}
	}
	identity := SchemaCacheIdentity(Config{Type: "sqlite", Database: "example.db", Password: "secret"})
	if identity != SchemaCacheIdentity(Config{Type: "sqlite", Database: "example.db", Password: "changed", FieldsCache: true}) {
		t.Fatal("密码轮换或开关变化不应使同库结构缓存失效")
	}
	content, err := database.ExportSchemaCache(ctx, identity, []string{"schema_items", "schema_items"})
	if err != nil || strings.Contains(string(content), "secret") {
		t.Fatalf("缓存导出失败或泄密: %v", err)
	}
	var decoded schemaCacheEnvelope
	if err := json.Unmarshal(content, &decoded); err != nil || len(decoded.Tables) != 1 {
		t.Fatalf("去重失败: %+v %v", decoded, err)
	}
	fresh, recorder := newRecordingHardeningSQLConnection(t)
	freshDB := NewDB(fresh)
	if err := freshDB.LoadSchemaCache(content, identity); err != nil {
		t.Fatal(err)
	}
	if info, err := freshDB.GetSchemaInfo(ctx, "schema_items", false); err != nil || len(info.Columns) != 3 || recorder.recordedQuery() != "" {
		t.Fatalf("跨连接元数据查询没有消费字段缓存: %#v %v", info, err)
	}
	if _, err := freshDB.Table("schema_items").Select(); err != nil {
		t.Fatal(err)
	}
	if query := recorder.recordedQuery(); !strings.HasPrefix(query, "SELECT * ") {
		t.Fatalf("查询应保留星号投影: %s", query)
	}
	if _, err := database.GetSchemaInfo(ctx, "missing_table", true); err == nil {
		t.Fatal("缺失表不能生成空字段缓存")
	}
}
