package sqllex

import "testing"

func TestMigrationStatementCommandHonorsDialectBoundaries(t *testing.T) {
	for _, test := range []struct {
		name, dialect, statement, want string
	}{
		{"MySQL 普通注释和尾部分隔符", "mysql", "/* 前导 */\n# ;\n-- ;\nUPDATE account SET name = 'a; b'; -- 尾注", "UPDATE"},
		{"MySQL CTE", "mysql", "WITH a AS (SELECT 1), b AS (SELECT 2) DELETE FROM accounts WHERE id IN (SELECT * FROM a)", "DELETE"},
		{"MySQL CTE 查询", "mysql", "WITH a AS (SELECT 1) SELECT * FROM a", "SELECT"},
		{"MySQL 不透明 WITH 语法", "mysql", "WITH x AS (SHOW TABLES)", "WITH"},
		{"MySQL 引用标识符", "mysql", "INSERT INTO `account;name` VALUES (\"a; b\")", "INSERT"},
		{"MySQL 两种模式一致的反斜线", "mysql", `UPDATE account SET note = 'folder\child'`, "UPDATE"},
		{"MySQL 不确定的反斜线", "mysql", `UPDATE account SET name = 'end\'; DROP TABLE accounts; -- '`, ""},
		{"MySQL 可执行注释", "mysql", "UPDATE account SET name='a' /*!50000 ; DROP TABLE account */", ""},
		{"MariaDB 可执行注释", "mysql", "UPDATE account SET name='a' /*M!100500 ; DROP TABLE account */", ""},
		{"短横线运算后堆叠语句", "mysql", "UPDATE accounts SET id=id--1; DROP TABLE accounts", ""},
		{"拒绝已知非事务写入关键词", "mysql", "INSERT DELAYED INTO accounts VALUES (1)", ""},
		{"拒绝 SELECT 输出文件", "mysql", "SELECT 1 INTO OUTFILE '/tmp/out'", ""},
		{"Oracle DML", "oracle", "MERGE INTO accounts a USING b ON (a.id=b.id) WHEN MATCHED THEN UPDATE SET a.value=b.value", "MERGE"},
		{"Oracle 替代引号", "oracle", "INSERT INTO a(id,v) VALUES (1,q'[a';b]')", "INSERT"},
		{"Oracle Unicode 替代引号", "oracle", "INSERT INTO a(id,v) VALUES (1,nq'字a';b字')", "INSERT"},
		{"Oracle 不闭合替代引号", "oracle", "UPDATE a SET v = q'[a; b'", ""},
		{"Oracle 引用标识符", "oracle", `UPDATE "account;name" SET "a" = 'x'';z'`, "UPDATE"},
		{"Oracle 不使用反引号", "oracle", "UPDATE `account` SET a = 1", ""},
		{"Oracle 不自动判定 WITH PL/SQL", "oracle", "WITH FUNCTION f RETURN NUMBER AS BEGIN RETURN 2; END; SELECT f FROM dual", ""},
		{"Oracle 重复分隔符", "oracle", "UPDATE a SET v = 1;;", ""},
		{"堆叠句", "oracle", "DELETE FROM a; --注释\nDROP TABLE a", ""},
		{"未闭合块注释", "oracle", "DELETE FROM a /* incomplete", ""},
		{"未闭合普通引用", "oracle", "UPDATE a SET v = 'quoted", ""},
		{"未闭合分组", "oracle", "DELETE FROM a WHERE id IN (1,2", ""},
		{"意外右括号", "oracle", "DELETE FROM a WHERE id=1)", ""},
		{"只有注释", "mysql", "# comment", ""},
		{"不支持的方言", "postgres", "UPDATE a SET v=1", ""},
		{"NUL", "mysql", "UPDATE a SET v=1\x00", ""},
		{"绕过关键字的前导标点", "mysql", "; UPDATE a SET v=1", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if actual := MigrationStatementCommand(test.statement, test.dialect); actual != test.want {
				t.Fatalf("命令为 %q，期望 %q", actual, test.want)
			}
		})
	}
}
