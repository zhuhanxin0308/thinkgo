//go:build oracle

package db

import (
	"testing"

	"github.com/mattn/go-sqlite3"
	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
)

// TestOracleCatalogIdentityProjection 在目录样本上执行实际投影，日常测试无需 OCI 服务。
func TestOracleCatalogIdentityProjection(t *testing.T) {
	_, raw := newModelScanSQLite(t)
	session, err := raw.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	err = session.Raw(func(driver any) error {
		conn := driver.(*sqlite3.SQLiteConn)
		if err := conn.RegisterFunc("SYS_CONTEXT", func(_, _ string) string { return "APP" }, true); err != nil {
			return err
		}
		return conn.RegisterFunc("NVL", func(value, fallback string) string {
			if value == "" {
				return fallback
			}
			return value
		}, true)
	})
	if closeErr := session.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`
CREATE TABLE all_tab_columns(owner, table_name, column_name, data_type, nullable, data_default, identity_column, column_id);
CREATE TABLE all_cons_columns(owner, constraint_name, table_name, column_name);
CREATE TABLE all_constraints(owner, constraint_name, constraint_type);
INSERT INTO all_tab_columns VALUES
 ('APP','ORDERS','ID','NUMBER','N',NULL,'YES',1), ('APP','ORDERS','AMOUNT','NUMBER','Y','7','NO',2),
 ('ARCHIVE','ORDERS','ID','NUMBER','N',NULL,'NO',1), ('ARCHIVE','ORDERS','AMOUNT','NUMBER','Y','7','NO',2);
INSERT INTO all_cons_columns VALUES ('APP','ORDERS_PK','ORDERS','ID'), ('ARCHIVE','ORDERS_PK','ORDERS','ID');
INSERT INTO all_constraints VALUES ('APP','ORDERS_PK','P'), ('ARCHIVE','ORDERS_PK','P');`)
	if err != nil {
		t.Fatal(err)
	}
	database := NewDB(NewSQLConnection(raw, &builder.Oracle{}))
	t.Cleanup(func() { _ = database.Close() })
	for table, identity := range map[string]bool{"orders": true, "app.orders": true, "archive.orders": false} {
		info, err := database.GetSchemaInfo(t.Context(), table, true)
		if err != nil || len(info.Columns) != 2 {
			t.Fatalf("%s 目录读取失败：%+v，%v", table, info, err)
		}
		id, amount := info.Columns[0], info.Columns[1]
		if id.Name != "ID" || !id.PrimaryKey || id.Nullable || id.AutoIncrement != identity ||
			amount.Name != "AMOUNT" || amount.PrimaryKey || !amount.Nullable || amount.AutoIncrement {
			t.Fatalf("%s 身份列、普通列或主键属性错误：%+v", table, info.Columns)
		}
	}
}
