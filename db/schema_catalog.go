package db

import (
	"fmt"
	"strings"
)

func splitSchemaTable(table string) (string, string, error) {
	parts := strings.Split(table, ".")
	if len(parts) > 2 {
		return "", "", fmt.Errorf("%w: 数据表只能包含一个数据库或模式前缀", ErrInvalidQuery)
	}
	for _, part := range parts {
		if part == "" || strings.TrimSpace(part) != part {
			return "", "", fmt.Errorf("%w: 数据表名称为空或包含空白", ErrInvalidQuery)
		}
		if err := validateIdentifier(part); err != nil || strings.ContainsAny(part, "* /\\-()\"'`") {
			return "", "", fmt.Errorf("%w: 数据表名称 %q 非法", ErrInvalidQuery, table)
		}
	}
	if len(parts) == 2 {
		return parts[0], parts[1], nil
	}
	return "", parts[0], nil
}

// schemaCatalogQuery 读取数据库自己的字段定义，所有外部表名使用绑定参数。
func schemaCatalogQuery(dialect, table string) (string, []interface{}, error) {
	namespace, name, err := splitSchemaTable(table)
	if err != nil {
		return "", nil, err
	}
	switch dialect {
	case "mysql":
		return "SELECT COLUMN_NAME AS name, COLUMN_TYPE AS data_type, IS_NULLABLE AS nullable, COLUMN_DEFAULT AS default_value, COLUMN_KEY AS primary_key, EXTRA AS extra FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = COALESCE(NULLIF(?, ''), DATABASE()) AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION", []interface{}{namespace, name}, nil
	case "postgres":
		return "SELECT a.attname AS name, pg_catalog.format_type(a.atttypid, a.atttypmod) AS data_type, NOT a.attnotnull AS nullable, pg_get_expr(d.adbin, d.adrelid) AS default_value, EXISTS (SELECT 1 FROM pg_index i WHERE i.indrelid = t.oid AND i.indisprimary AND a.attnum = ANY(i.indkey)) AS primary_key, (a.attidentity <> '' OR pg_get_serial_sequence(format('%I.%I', n.nspname, t.relname), a.attname) IS NOT NULL) AS auto_increment FROM pg_attribute a JOIN pg_class t ON t.oid = a.attrelid JOIN pg_namespace n ON n.oid = t.relnamespace LEFT JOIN pg_attrdef d ON d.adrelid = t.oid AND d.adnum = a.attnum WHERE t.relname = ? AND n.nspname = COALESCE(NULLIF(?, ''), current_schema()) AND a.attnum > 0 AND NOT a.attisdropped ORDER BY a.attnum", []interface{}{name, namespace}, nil
	case "sqlite":
		if namespace == "" {
			namespace = "main"
		}
		return `SELECT name, type AS data_type, "notnull" AS not_null, dflt_value AS default_value, pk AS primary_key FROM pragma_table_info(?, ?) ORDER BY cid`, []interface{}{name, namespace}, nil
	case "sqlserver":
		return "SELECT c.name AS name, t.name AS data_type, c.is_nullable AS nullable, OBJECT_DEFINITION(c.default_object_id) AS default_value, c.is_identity AS auto_increment, CASE WHEN EXISTS (SELECT 1 FROM sys.index_columns ic JOIN sys.indexes i ON i.object_id = ic.object_id AND i.index_id = ic.index_id WHERE ic.object_id = c.object_id AND ic.column_id = c.column_id AND i.is_primary_key = 1) THEN 1 ELSE 0 END AS primary_key FROM sys.columns c JOIN sys.types t ON t.user_type_id = c.user_type_id WHERE c.object_id = OBJECT_ID(?) ORDER BY c.column_id", []interface{}{table}, nil
	case "oracle":
		return `SELECT c.column_name AS "name", c.data_type AS "data_type", c.nullable AS "nullable", c.data_default AS "default_value", CASE WHEN EXISTS (SELECT 1 FROM all_cons_columns cc JOIN all_constraints k ON k.owner = cc.owner AND k.constraint_name = cc.constraint_name WHERE k.constraint_type = 'P' AND cc.owner = c.owner AND cc.table_name = c.table_name AND cc.column_name = c.column_name) THEN 1 ELSE 0 END AS "primary_key" FROM all_tab_columns c WHERE c.owner = NVL(?, SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA')) AND c.table_name = ? ORDER BY c.column_id`, []interface{}{strings.ToUpper(namespace), strings.ToUpper(name)}, nil
	default:
		return "", nil, fmt.Errorf("%w: 方言 %q 不支持关系型结构缓存", ErrInvalidQuery, dialect)
	}
}

func schemaTablesQuery(dialect, namespace string) (string, []interface{}, error) {
	if namespace != "" {
		if _, _, err := splitSchemaTable(namespace); err != nil || strings.Contains(namespace, ".") {
			return "", nil, fmt.Errorf("%w: 数据库或模式名称非法", ErrInvalidQuery)
		}
	}
	switch dialect {
	case "mysql":
		return "SELECT TABLE_NAME AS name FROM information_schema.TABLES WHERE TABLE_SCHEMA = COALESCE(NULLIF(?, ''), DATABASE()) AND TABLE_TYPE = 'BASE TABLE' ORDER BY TABLE_NAME", []interface{}{namespace}, nil
	case "postgres":
		return "SELECT table_name AS name FROM information_schema.tables WHERE table_schema = COALESCE(NULLIF(?, ''), current_schema()) AND table_type = 'BASE TABLE' ORDER BY table_name", []interface{}{namespace}, nil
	case "sqlite":
		if namespace == "" {
			namespace = "main"
		}
		return `SELECT name FROM "` + namespace + `".sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`, nil, nil
	case "sqlserver":
		return "SELECT TABLE_NAME AS name FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = COALESCE(NULLIF(?, ''), SCHEMA_NAME()) AND TABLE_TYPE = 'BASE TABLE' ORDER BY TABLE_NAME", []interface{}{namespace}, nil
	case "oracle":
		return `SELECT table_name AS "name" FROM all_tables WHERE owner = NVL(?, SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA')) ORDER BY table_name`, []interface{}{strings.ToUpper(namespace)}, nil
	default:
		return "", nil, fmt.Errorf("%w: 方言 %q 不支持数据表枚举", ErrInvalidQuery, dialect)
	}
}
