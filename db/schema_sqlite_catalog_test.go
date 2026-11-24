package db

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
)

func TestSQLiteSchemaTablesLiteralPrefix(t *testing.T) {
	database, raw := newModelScanSQLite(t)
	names := []string{"orders", "sqlite", "sqliteOrders", "SQLiteArchive"}
	for _, name := range names {
		if _, err := raw.Exec("CREATE TABLE " + name + " (id INTEGER PRIMARY KEY AUTOINCREMENT)"); err != nil {
			t.Fatal(err)
		}
	}
	want := append(names, "scan_records")
	slices.Sort(want)
	for _, namespace := range []string{"", "main"} {
		tables, err := database.SchemaTables(t.Context(), namespace)
		if err != nil {
			t.Fatal(err)
		}
		expected := slices.Clone(want)
		if namespace != "" {
			for i := range expected {
				expected[i] = namespace + "." + expected[i]
			}
		}
		if !slices.Equal(tables, expected) {
			t.Fatalf("模式 %q 枚举结果为 %v，期望 %v", namespace, tables, expected)
		}
		for _, selected := range [][]string{tables, {"sqliteOrders"}} {
			content, err := database.ExportSchemaCache(t.Context(), "catalog", selected)
			if err != nil {
				t.Fatal(err)
			}
			var exported schemaCacheEnvelope
			if err := json.Unmarshal(content, &exported); err != nil {
				t.Fatal(err)
			}
			var exportedNames []string
			for _, table := range exported.Tables {
				exportedNames = append(exportedNames, table.Table)
			}
			if !slices.Equal(exportedNames, selected) {
				t.Fatalf("导出表清单为 %v，期望 %v", exportedNames, selected)
			}
		}
	}
}

func TestSQLiteSchemaVisibleGeneratedColumns(t *testing.T) {
	database, raw := newModelScanSQLite(t)
	_, err := raw.Exec(`CREATE TABLE items (
		doubled INTEGER GENERATED ALWAYS AS (quantity * 2) VIRTUAL,
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		total INTEGER GENERATED ALWAYS AS (quantity * price) STORED NOT NULL,
		quantity INTEGER NOT NULL,
		price INTEGER NOT NULL
	)`)
	if err != nil {
		t.Fatal(err)
	}
	assertSQLiteCatalogRoundTrip(t, database, "main.items", []SchemaColumn{
		{Name: "doubled", DataType: "INTEGER", Nullable: true},
		{Name: "id", DataType: "INTEGER", PrimaryKey: true, AutoIncrement: true},
		{Name: "total", DataType: "INTEGER"},
		{Name: "quantity", DataType: "INTEGER"},
		{Name: "price", DataType: "INTEGER"},
	})

	// 内置虚拟表自身有两个隐藏参数列；其他列应与其星号投影一致。
	rows, err := raw.Query("SELECT * FROM pragma_table_info('items')")
	if err != nil {
		t.Fatal(err)
	}
	visible, err := rows.Columns()
	if closeErr := rows.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	schema, err := database.GetSchemaInfo(t.Context(), "pragma_table_info", true)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, column := range schema.Columns {
		names = append(names, column.Name)
	}
	if !slices.Equal(names, visible) {
		t.Fatalf("虚拟表可见字段为 %v，期望 %v", names, visible)
	}
}

func TestSQLiteSchemaPrimaryKeyNullability(t *testing.T) {
	for _, tc := range []struct {
		name, columns, suffix string
		nullable, auto        bool
	}{
		{"行号别名", "id INTEGER PRIMARY KEY, note TEXT UNIQUE", "", false, false},
		{"显式自增", "id INTEGER PRIMARY KEY AUTOINCREMENT, note TEXT", "", false, true},
		{"列级降序", "id INTEGER PRIMARY KEY DESC, note TEXT", "", true, false},
		{"表级降序", "id INTEGER, note TEXT, PRIMARY KEY(id DESC)", "", false, false},
		{"文本主键", "id TEXT PRIMARY KEY, note TEXT", "", true, false},
		{"整数亲和类型", "id INT PRIMARY KEY, note TEXT", "", true, false},
		{"复合主键", "id INTEGER, note TEXT, PRIMARY KEY (id, note)", "", true, false},
		{"严格表", "id TEXT PRIMARY KEY, note TEXT", " STRICT", false, false},
		{"无行号表", "id INTEGER PRIMARY KEY, note TEXT", " WITHOUT ROWID", false, false},
		{"显式非空", "id TEXT NOT NULL PRIMARY KEY, note TEXT", "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database, raw := newModelScanSQLite(t)
			if _, err := raw.Exec("CREATE TABLE items (" + tc.columns + ")" + tc.suffix); err != nil {
				t.Fatal(err)
			}
			var dataType string
			var notePK bool
			if err := raw.QueryRow("SELECT type FROM pragma_table_info('items') WHERE name = 'id'").Scan(&dataType); err != nil {
				t.Fatal(err)
			}
			if err := raw.QueryRow("SELECT pk > 0 FROM pragma_table_info('items') WHERE name = 'note'").Scan(&notePK); err != nil {
				t.Fatal(err)
			}
			assertSQLiteCatalogRoundTrip(t, database, "items", []SchemaColumn{
				{Name: "id", DataType: dataType, PrimaryKey: true, Nullable: tc.nullable, AutoIncrement: tc.auto},
				{Name: "note", DataType: "TEXT", PrimaryKey: notePK, Nullable: true},
			})
		})
	}
}

func assertSQLiteCatalogRoundTrip(t *testing.T, database *DB, table string, columns []SchemaColumn) {
	t.Helper()
	want := TableSchema{Table: table, Columns: columns}
	check := func(database *DB, refresh bool) {
		t.Helper()
		got, err := database.GetSchemaInfo(t.Context(), table, refresh)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s 字段定义不符：得到 %+v，期望 %+v，错误 %v", table, got, want, err)
		}
	}
	check(database, true)
	check(database, false)
	const identity = "sqlite-catalog"
	content, err := database.ExportSchemaCache(t.Context(), identity, []string{table})
	if err != nil {
		t.Fatal(err)
	}
	fresh, _ := newModelScanSQLite(t)
	if err := fresh.LoadSchemaCache(content, identity); err != nil {
		t.Fatal(err)
	}
	check(fresh, false)
}
