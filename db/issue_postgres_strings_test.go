package db

import (
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
)

// TestPostgresStringValidationParity 验证参数校验、重绑定和脱敏使用一致的字符串边界。
func TestPostgresStringValidationParity(t *testing.T) {
	for _, test := range []struct {
		name  string
		query string
		want  string
		count int
	}{
		{"普通字符串", `SELECT '\', ?`, `SELECT '\', $1`, 1},
		{"引用标识符", `SELECT "column\", ? FROM t`, `SELECT "column\", $1 FROM t`, 1},
		{"小写前缀", `SELECT e'it\'s ?', ?`, `SELECT e'it\'s ?', $1`, 1},
		{"注释续接", "SELECT E'first' -- ?\n'it\\'s ?', ?", "SELECT E'first' -- ?\n'it\\'s ?', $1", 1},
		{"嵌套注释续接", "SELECT E'first' /* outer /* ? */\n */ 'it\\'s ?', ?", "SELECT E'first' /* outer /* ? */\n */ 'it\\'s ?', $1", 1},
		{"普通跨行", "SELECT 'first'\n'\\', ?", "SELECT 'first'\n'\\', $1", 1},
		{"转义模式结束", `SELECT E'first', '\', ?`, `SELECT E'first', '\', $1`, 1},
		{"数组与嵌套注释", `SELECT ARRAY['\', ? /* a /* b */ ? */], ?`, `SELECT ARRAY['\', $1 /* a /* b */ ? */], $2`, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			count, err := countSQLPlaceholders(test.query, "postgres")
			if err != nil || count != test.count {
				t.Fatalf("占位符计数错误: %d %v", count, err)
			}
			if err := validateRawStatement(test.query, count, "postgres"); err != nil {
				t.Fatal(err)
			}
			if got := (&builder.Pgsql{}).Rebind(test.query); got != test.want {
				t.Fatalf("重绑定边界不一致: %q", got)
			}
		})
	}
	for _, sqlText := range []string{
		"SELECT E'first'\n'it\\'s PRIVATE_MARKER ?', ?",
		"SELECT E'first' /* ?\n */ 'it\\'s PRIVATE_MARKER ?', ?",
		`SELECT "column\", 'PRIVATE_MARKER', ?`,
	} {
		redacted := redactSQLLiteralsAndComments(sqlText, "postgres")
		if strings.Contains(redacted, "PRIVATE_MARKER") || !strings.HasSuffix(redacted, ", ?") {
			t.Fatalf("脱敏泄露内容或吞掉正文: %q", redacted)
		}
	}
	for _, invalid := range []string{`SELECT E'\', ?`, `SELECT 'unclosed ?`, "SELECT E'a'\n'unclosed", `SELECT 'closed' /* unclosed`} {
		if _, err := countSQLPlaceholders(invalid, "postgres"); err == nil {
			t.Fatalf("非法字符串或注释未拒绝: %q", invalid)
		}
	}
}

// FuzzPostgresStringParameterIsolation 用合法封装的任意内容检查正文参数及脱敏边界。
func FuzzPostgresStringParameterIsolation(f *testing.F) {
	for _, seed := range []string{"?", `\`, "it's ?", "/* ? */", "文本", "\n", ""} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if len(value) > 512 || strings.ContainsRune(value, 0) {
			return
		}
		standard := "'" + strings.ReplaceAll(value, "'", "''") + "'"
		escaped := "E'" + strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), "'", "''") + "'"
		for _, literal := range []string{standard, escaped, "E'first'\n" + escaped[1:]} {
			query := "SELECT " + literal + ", ARRAY[?, ?], ?"
			if count, err := countSQLPlaceholders(query, "postgres"); err != nil || count != 3 {
				t.Fatalf("字面量污染参数边界: %q count=%d err=%v", query, count, err)
			}
			want := "SELECT " + literal + ", ARRAY[$1, $2], $3"
			if got := (&builder.Pgsql{}).Rebind(query); got != want {
				t.Fatalf("字面量污染重绑定: %q", got)
			}
		}
	})
}
