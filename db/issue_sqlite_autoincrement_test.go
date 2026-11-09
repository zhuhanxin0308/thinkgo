package db

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// TestSQLiteSchemaExplicitAutoIncrement 验证真实 SQLite 的显式声明、引用边界和缓存往返。
func TestSQLiteSchemaExplicitAutoIncrement(t *testing.T) {
	cases := []struct {
		name string
		ddl  string
		auto bool
	}{
		{"列级自增", "CREATE TABLE items(id INTEGER PRIMARY KEY AUTOINCREMENT, note TEXT)", true},
		{"表级自增", "CREATE TABLE items(id INTEGER, note TEXT, PRIMARY KEY(id AUTOINCREMENT))", true},
		{"排序与冲突策略", "CREATE TABLE items(id INTEGER PRIMARY KEY ASC ON CONFLICT REPLACE AUTOINCREMENT, note TEXT)", true},
		{"混合大小写和引号", "CREATE TABLE \"items\"(\"id\" integer primary key autoIncrement, [note] TEXT)", true},
		{"主键不在首列", "CREATE TABLE items(note TEXT, id INTEGER PRIMARY KEY AUTOINCREMENT)", true},
		{"单引号标识符", "CREATE TABLE 'items'('id' INTEGER PRIMARY KEY AUTOINCREMENT, note TEXT)", true},
		{"反引号标识符", "CREATE TABLE `items`(`id` INTEGER PRIMARY KEY AUTOINCREMENT, note TEXT)", true},
		{"隐式行号", "CREATE TABLE items(id INTEGER PRIMARY KEY, note TEXT)", false},
		{"普通主键", "CREATE TABLE items(id TEXT PRIMARY KEY, note TEXT)", false},
		{"复合主键", "CREATE TABLE items(id INTEGER, note TEXT, PRIMARY KEY(id, note))", false},
		{"无主键", "CREATE TABLE items(id INTEGER, note TEXT)", false},
		{"无行号", "CREATE TABLE items(id INTEGER PRIMARY KEY, note TEXT) WITHOUT ROWID", false},
		{"字符串和转义", "CREATE TABLE items(id INTEGER PRIMARY KEY, note TEXT DEFAULT 'can''t AUTOINCREMENT, )')", false},
		{"反斜杠不是转义", "CREATE TABLE items(id INTEGER PRIMARY KEY, note TEXT DEFAULT '\\', other TEXT DEFAULT 'AUTOINCREMENT')", false},
		{"带引号关键字列名", "CREATE TABLE items(id INTEGER PRIMARY KEY, \"AUTOINCREMENT\" TEXT, note TEXT)", false},
		{"带引号类型名", "CREATE TABLE items(id INTEGER PRIMARY KEY, note \"AUTOINCREMENT\")", false},
		{"关键字子串", "CREATE TABLE items(id INTEGER PRIMARY KEY, note AUTOINCREMENTED)", false},
		{"块注释", "CREATE TABLE items(id INTEGER PRIMARY KEY /* AUTOINCREMENT */, note TEXT)", false},
		{"行注释", "CREATE TABLE items(id INTEGER PRIMARY KEY -- AUTOINCREMENT\n, note TEXT)", false},
		{"回车不结束行注释", "CREATE TABLE items(id INTEGER PRIMARY KEY -- comment\r AUTOINCREMENT\n, note TEXT)", false},
		{"注释分隔合法关键字", "CREATE TABLE items(id INTEGER PRIMARY /* comment */ KEY -- comment\n AUTOINCREMENT, note TEXT)", true},
		{"生成列占首位置", "CREATE TABLE items(note TEXT GENERATED ALWAYS AS ('AUTOINCREMENT') VIRTUAL, id INTEGER PRIMARY KEY AUTOINCREMENT)", true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			database, raw := newModelScanSQLite(t)
			if _, err := raw.Exec(testCase.ddl); err != nil {
				t.Fatal(err)
			}
			for _, table := range []string{"items", "main.items", "main.ItEmS"} {
				for _, refresh := range []bool{true, false} {
					info, err := database.GetSchemaInfo(t.Context(), table, refresh)
					if err != nil {
						t.Fatal(err)
					}
					assertSQLiteAutoIncrement(t, info, testCase.auto)
				}
			}
			const identity = "sqlite-autoincrement-regression"
			content, err := database.ExportSchemaCache(t.Context(), identity, []string{"main.items"})
			if err != nil {
				t.Fatal(err)
			}
			var exported schemaCacheEnvelope
			if err := json.Unmarshal(content, &exported); err != nil || len(exported.Tables) != 1 {
				t.Fatalf("结构缓存导出失败: %v", err)
			}
			assertSQLiteAutoIncrement(t, exported.Tables[0], testCase.auto)
			fresh, recorder := newRecordingHardeningSQLConnection(t)
			reloaded := NewDB(fresh)
			t.Cleanup(func() { _ = reloaded.Close() })
			if err := reloaded.LoadSchemaCache(content, identity); err != nil {
				t.Fatal(err)
			}
			info, err := reloaded.GetSchemaInfo(t.Context(), "main.items", false)
			if err != nil {
				t.Fatal(err)
			}
			assertSQLiteAutoIncrement(t, info, testCase.auto)
			if recorder.recordedQuery() != "" {
				t.Fatal("导入缓存后不应重新访问数据库")
			}
		})
	}
}

func assertSQLiteAutoIncrement(t *testing.T, schema TableSchema, expected bool) {
	t.Helper()
	foundID := false
	for _, column := range schema.Columns {
		foundID = foundID || column.Name == "id"
		want := expected && column.Name == "id"
		if column.AutoIncrement != want {
			t.Fatalf("字段 %s 的自增属性错误: got=%t want=%t schema=%+v", column.Name, column.AutoIncrement, want, schema)
		}
	}
	if !foundID {
		t.Fatalf("结构信息缺少测试主键字段: %+v", schema)
	}
}

// TestSQLiteSchemaCatalogReturnsDDLOnce 验证多列查询不重复传输 DDL，视图仍可正常获取字段。
func TestSQLiteSchemaCatalogReturnsDDLOnce(t *testing.T) {
	database, raw := newModelScanSQLite(t)
	if _, err := raw.Exec("CREATE TABLE items(id INTEGER PRIMARY KEY AUTOINCREMENT, note TEXT, amount INTEGER); CREATE VIEW item_view AS SELECT id, note FROM items"); err != nil {
		t.Fatal(err)
	}
	query, args, err := schemaCatalogQuery("sqlite", "main.items")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := database.QueryContext(t.Context(), query, args...)
	if err != nil {
		t.Fatal(err)
	}
	ddlCount := 0
	for _, row := range rows {
		if schemaValueText(row["create_sql"]) != "" {
			ddlCount++
		}
	}
	if len(rows) != 3 || ddlCount != 1 {
		t.Fatalf("目录查询重复或遗漏 DDL: rows=%d ddl=%d", len(rows), ddlCount)
	}
	view, err := database.GetSchemaInfo(t.Context(), "item_view", true)
	if err != nil {
		t.Fatal(err)
	}
	assertSQLiteAutoIncrement(t, view, false)
}

// TestSQLiteSchemaAutoIncrementNamespaces 验证同名主表、临时表及附加库不会串用 DDL。
func TestSQLiteSchemaAutoIncrementNamespaces(t *testing.T) {
	database, raw := newModelScanSQLite(t)
	for _, statement := range []string{
		"ATTACH DATABASE ':memory:' AS auxiliary",
		"CREATE TABLE main.items(id INTEGER PRIMARY KEY, note TEXT)",
		"CREATE TABLE temp.items(id INTEGER PRIMARY KEY AUTOINCREMENT, note TEXT)",
		"CREATE TABLE auxiliary.items(id INTEGER PRIMARY KEY AUTOINCREMENT, note TEXT)",
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	for table, expected := range map[string]bool{"items": false, "main.items": false, "temp.items": true, "auxiliary.items": true} {
		info, err := database.GetSchemaInfo(t.Context(), table, true)
		if err != nil {
			t.Fatal(err)
		}
		assertSQLiteAutoIncrement(t, info, expected)
	}
	if _, err := raw.Exec("DROP TABLE main.items; CREATE TABLE main.items(id INTEGER PRIMARY KEY AUTOINCREMENT, note TEXT)"); err != nil {
		t.Fatal(err)
	}
	stale, err := database.GetSchemaInfo(t.Context(), "main.items", false)
	if err != nil {
		t.Fatal(err)
	}
	assertSQLiteAutoIncrement(t, stale, false)
	fresh, err := database.GetSchemaInfo(t.Context(), "main.items", true)
	if err != nil {
		t.Fatal(err)
	}
	assertSQLiteAutoIncrement(t, fresh, true)
	fresh.Columns[0].AutoIncrement = false
	again, err := database.GetSchemaInfo(t.Context(), "main.items", false)
	if err != nil || reflect.DeepEqual(again, fresh) {
		t.Fatalf("调用方修改污染了共享结构缓存: %+v %v", again, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := database.GetSchemaInfo(ctx, "main.items", true); !errors.Is(err, context.Canceled) {
		t.Fatalf("已取消查询未返回上下文错误: %v", err)
	}
}
