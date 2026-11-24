package db

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// sqliteSchemaCatalogQuery 在同一语句、同一物理连接中读取字段和建表声明。
// namespace 已经过 splitSchemaTable 校验，表名始终绑定；DDL 仅返回一次，避免按字段数放大数据量。
func sqliteSchemaCatalogQuery(namespace, name string) (string, []interface{}) {
	// table_xinfo 的 hidden=1 是虚拟表隐藏列，生成列仍属于可见字段。
	// 普通表中，只有行号别名主键没有独立的 pk 索引；列级 DESC 和复合主键均有该索引。
	query := `WITH columns AS (SELECT * FROM pragma_table_xinfo(?, ?) WHERE hidden <> 1),
     table_def AS (SELECT sql, rootpage FROM "` + namespace + `".sqlite_master
                   WHERE type = 'table' AND name = ? COLLATE NOCASE)
SELECT name, type AS data_type,
       CASE WHEN pk = 1 AND type = 'INTEGER' COLLATE NOCASE AND (SELECT rootpage FROM table_def) > 0
                 AND NOT EXISTS (SELECT 1 FROM pragma_index_list(?, ?) WHERE origin = 'pk')
            THEN 1 ELSE "notnull" END AS not_null,
       dflt_value AS default_value, pk AS primary_key,
       CASE WHEN cid = (SELECT MIN(cid) FROM columns)
            THEN (SELECT sql FROM table_def) END AS create_sql
FROM columns ORDER BY cid`
	return query, []interface{}{name, namespace, name, name, namespace}
}

// applySQLiteAutoIncrement 结合 SQLite 已验证的 DDL 与 PRAGMA 主键信息确定目标列。
// AUTOINCREMENT 是 SQLite 的保留语法，只能用于唯一 INTEGER PRIMARY KEY，且不属于可回退的标识符。
// 因此词法识别同时覆盖列级和表级主键，不能把普通 INTEGER PRIMARY KEY 当成显式自增。
func applySQLiteAutoIncrement(schema *TableSchema, ddl string) error {
	explicit, err := sqliteSchemaHasAutoIncrement(ddl)
	if err != nil || !explicit {
		return err
	}
	primaryIndex := -1
	for index, column := range schema.Columns {
		if !column.PrimaryKey {
			continue
		}
		if primaryIndex >= 0 || !strings.EqualFold(column.DataType, "INTEGER") {
			return fmt.Errorf("%w: SQLite 自增声明与主键元数据不一致", ErrInvalidDatabaseRow)
		}
		primaryIndex = index
	}
	if primaryIndex < 0 {
		return fmt.Errorf("%w: SQLite 自增声明缺少主键元数据", ErrInvalidDatabaseRow)
	}
	schema.Columns[primaryIndex].AutoIncrement = true
	return nil
}

// sqliteSchemaHasAutoIncrement 只处理数据库目录中的普通 CREATE TABLE 声明，不解释虚拟表参数。
func sqliteSchemaHasAutoIncrement(ddl string) (bool, error) {
	lexer := sqliteSchemaLexer{text: ddl}
	for _, keyword := range []string{"CREATE", "TABLE"} {
		token, word, err := lexer.next()
		if err != nil {
			return false, err
		}
		if !word || !strings.EqualFold(token, keyword) {
			return false, nil
		}
	}
	found := false
	for {
		token, word, err := lexer.next()
		if err != nil {
			return false, err
		}
		if token == "" {
			return found, nil
		}
		if word && strings.EqualFold(token, "AUTOINCREMENT") {
			found = true
		}
	}
}

// sqliteSchemaLexer 流式扫描目录 SQL，保留标识符边界，不分配完整 token 列表。
type sqliteSchemaLexer struct {
	text  string
	index int
}

func (lexer *sqliteSchemaLexer) next() (string, bool, error) {
	for lexer.index < len(lexer.text) {
		start := lexer.index
		current := lexer.text[start]
		if strings.ContainsRune(" \t\n\r\f\v", rune(current)) {
			lexer.index++
			continue
		}
		if current == '-' && start+1 < len(lexer.text) && lexer.text[start+1] == '-' {
			// SQLite 行注释只在 LF 结束，CR 本身仍属于注释内容。
			lexer.index += len("--")
			for lexer.index < len(lexer.text) && lexer.text[lexer.index] != '\n' {
				lexer.index++
			}
			continue
		}
		if current == '/' && start+1 < len(lexer.text) && lexer.text[start+1] == '*' {
			// SQLite 块注释不嵌套，允许延伸到输入末尾。
			lexer.index, _ = skipSQLBlockComment(lexer.text, start, "sqlite")
			continue
		}
		if current == '\'' || current == '"' || current == '`' || current == '[' {
			return lexer.quoted()
		}
		lexer.index++
		if sqliteSchemaWordByte(current) {
			for lexer.index < len(lexer.text) && sqliteSchemaWordByte(lexer.text[lexer.index]) {
				lexer.index++
			}
			return lexer.text[start:lexer.index], true, nil
		}
		return lexer.text[start:lexer.index], false, nil
	}
	return "", false, nil
}

func (lexer *sqliteSchemaLexer) quoted() (string, bool, error) {
	start := lexer.index
	closing := lexer.text[start]
	bracket := closing == '['
	if bracket {
		closing = ']'
	}
	lexer.index++
	for lexer.index < len(lexer.text) {
		current := lexer.text[lexer.index]
		lexer.index++
		if current != closing {
			continue
		}
		// 单/双引号与反引号通过重复闭合符转义；方括号在第一个 ] 结束，反斜杠无转义含义。
		if !bracket && lexer.index < len(lexer.text) && lexer.text[lexer.index] == closing {
			lexer.index++
			continue
		}
		return lexer.text[start:lexer.index], false, nil
	}
	return "", false, fmt.Errorf("%w: SQLite 建表声明包含未闭合引用", ErrInvalidDatabaseRow)
}

func sqliteSchemaWordByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' || value == '_' || value == '$' || value >= utf8.RuneSelf
}
