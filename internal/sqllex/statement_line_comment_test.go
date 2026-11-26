package sqllex

import "testing"

// TestMigrationStatementMySQLLineCommentBoundaries 验证独立 CR 不改变 MySQL 注释的词法边界。
func TestMigrationStatementMySQLLineCommentBoundaries(t *testing.T) {
	const update = "UPDATE accounts SET active = 1"
	for _, prefix := range []string{"-- ", "# "} {
		t.Run(prefix, func(t *testing.T) {
			for _, test := range []struct {
				name, statement, want string
			}{
				{"LF", prefix + "note\n" + update, "UPDATE"},
				{"CRLF", prefix + "note\r\n" + update, "UPDATE"},
				{"注释正文内 CR", prefix + "note\rcontinued\n" + update, "UPDATE"},
				{"尾部注释内 CR", update + "; " + prefix + "note\rcontinued", "UPDATE"},
				{"无 LF 的注释", prefix + "note\r" + update, ""},
				{"LF 后第二条语句", update + "; " + prefix + "note\rcontinued\nSELECT 1", ""},
			} {
				t.Run(test.name, func(t *testing.T) {
					if got := MigrationStatementCommand(test.statement, "mysql"); got != test.want {
						t.Fatalf("分类=%q，期望=%q", got, test.want)
					}
				})
			}
		})
	}
	// Oracle 的独立 CR 仍终止行注释，不能误用 MySQL 的规则。
	if got := MigrationStatementCommand("-- note\r"+update, "oracle"); got != "UPDATE" {
		t.Fatalf("Oracle CR 边界改变: %q", got)
	}
}
