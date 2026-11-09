package db

import (
	"errors"
	"strings"
	"testing"
)

// TestSQLiteSchemaKeywordBoundaries 验证目录 DDL 的词法边界，引用内容不能冒充保留关键字。
func TestSQLiteSchemaKeywordBoundaries(t *testing.T) {
	cases := []struct {
		name string
		ddl  string
		want bool
	}{
		{"空目录值", "", false},
		{"视图", "CREATE VIEW items AS SELECT 'AUTOINCREMENT'", false},
		{"虚拟表参数", "CREATE VIRTUAL TABLE items USING module(AUTOINCREMENT)", false},
		{"注释前缀与分隔", "/* header */ CREATE -- header\n TABLE items(id INTEGER PRIMARY KEY AUTOINCREMENT)", true},
		{"表名和列名", "CREATE TABLE \"AUTOINCREMENT\"(\"AUTOINCREMENT\" INTEGER PRIMARY KEY)", false},
		{"双引号转义", "CREATE TABLE items(id INTEGER PRIMARY KEY, note TEXT DEFAULT \"a\"\"AUTOINCREMENT\")", false},
		{"反引号转义", "CREATE TABLE items(id INTEGER PRIMARY KEY, `a``AUTOINCREMENT` TEXT)", false},
		{"方括号", "CREATE TABLE [items]([id] INTEGER PRIMARY KEY, [AUTOINCREMENT] TEXT)", false},
		{"关键字两侧标识符", "CREATE TABLE items(id INTEGER PRIMARY KEY, xAUTOINCREMENT TEXT, AUTOINCREMENT_x TEXT)", false},
		{"非 ASCII 与美元边界", "CREATE TABLE items(id INTEGER PRIMARY KEY, 名AUTOINCREMENT TEXT, dollar$AUTOINCREMENT TEXT)", false},
		{"注释中嵌套起始符", "CREATE TABLE items(id INTEGER PRIMARY KEY /* outer /* inner */ AUTOINCREMENT)", true},
		{"尾随块注释", "CREATE TABLE items(id INTEGER PRIMARY KEY AUTOINCREMENT) /* unfinished", true},
		{"尾随行注释", "CREATE TABLE items(id INTEGER PRIMARY KEY) -- AUTOINCREMENT", false},
		{"回车仍在行注释内", "CREATE TABLE items(id INTEGER PRIMARY KEY -- x\r AUTOINCREMENT\n)", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := sqliteSchemaHasAutoIncrement(testCase.ddl)
			if err != nil || got != testCase.want {
				t.Fatalf("目录关键字识别错误: got=%t want=%t err=%v", got, testCase.want, err)
			}
		})
	}
	for _, ddl := range []string{
		"\"CREATE",
		"CREATE \"TABLE",
		"CREATE TABLE items(id INTEGER PRIMARY KEY AUTOINCREMENT, note TEXT DEFAULT 'unterminated)",
		"CREATE TABLE items([id INTEGER PRIMARY KEY AUTOINCREMENT)",
	} {
		if found, err := sqliteSchemaHasAutoIncrement(ddl); found || !errors.Is(err, ErrInvalidDatabaseRow) {
			t.Fatalf("损坏引用不应生成可信结构缓存: found=%t err=%v", found, err)
		}
	}
}

// TestSQLiteAutoIncrementRequiresConsistentPrimaryKey 验证异常元数据不会把自增属性安装到错误列。
func TestSQLiteAutoIncrementRequiresConsistentPrimaryKey(t *testing.T) {
	const ddl = "CREATE TABLE items(id INTEGER PRIMARY KEY AUTOINCREMENT)"
	for _, columns := range [][]SchemaColumn{
		{{Name: "id", DataType: "INTEGER"}},
		{{Name: "id", DataType: "TEXT", PrimaryKey: true}},
		{{Name: "id", DataType: "INTEGER", PrimaryKey: true}, {Name: "tenant", DataType: "INTEGER", PrimaryKey: true}},
	} {
		schema := TableSchema{Table: "items", Columns: columns}
		if err := applySQLiteAutoIncrement(&schema, ddl); !errors.Is(err, ErrInvalidDatabaseRow) {
			t.Fatalf("不一致元数据未拒绝: %v", err)
		}
		for _, column := range schema.Columns {
			if column.AutoIncrement {
				t.Fatal("失败时不应部分写入自增状态")
			}
		}
	}
	schema := TableSchema{Table: "items", Columns: []SchemaColumn{{Name: "id", DataType: "integer", PrimaryKey: true}}}
	if err := applySQLiteAutoIncrement(&schema, ddl); err != nil || !schema.Columns[0].AutoIncrement {
		t.Fatalf("合法大小写元数据未识别: %v", err)
	}
}

// BenchmarkSQLiteSchemaKeywordScan 衡量长注释和字符串的扫描成本，防止引入反复回溯或完整 token 分配。
func BenchmarkSQLiteSchemaKeywordScan(b *testing.B) {
	ddl := "CREATE TABLE items(id INTEGER PRIMARY KEY AUTOINCREMENT, note TEXT DEFAULT '" +
		strings.Repeat("AUTOINCREMENT,''quoted'',", 4096) + "')"
	b.SetBytes(int64(len(ddl)))
	b.ReportAllocs()
	for b.Loop() {
		found, err := sqliteSchemaHasAutoIncrement(ddl)
		if err != nil || !found {
			b.Fatalf("扫描错误: %v", err)
		}
	}
}
