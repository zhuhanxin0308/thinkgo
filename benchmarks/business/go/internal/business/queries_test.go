package business

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContractPlaceholderBinding(t *testing.T) {
	for _, sample := range []struct{ input, want string }{
		{"SELECT ?::uuid, ?", "SELECT $1::uuid, $2"},
		{"SELECT '?', 'it''s ?', \"?\", ?", "SELECT '?', 'it''s ?', \"?\", $1"},
		{"SELECT 1", "SELECT 1"},
	} {
		got, err := postgresQuery(sample.input)
		if err != nil || got != sample.want {
			t.Fatalf("绑定错误: %q, %v", got, err)
		}
	}
	for _, invalid := range []string{"SELECT 'unfinished", "SELECT \"unfinished", "SELECT $$?$$", "SELECT ? -- ?", "SELECT /* ? */ ?"} {
		if _, err := postgresQuery(invalid); err == nil {
			t.Fatalf("合同不支持的SQL未被拒绝: %s", invalid)
		}
	}
}

func TestQueryContractValidation(t *testing.T) {
	queries, err := loadQueries(filepath.Join("..", "..", "..", "contracts", "queries.json"))
	if err != nil || len(queries) != 14 || !strings.Contains(queries["catalog"], "$7") {
		t.Fatalf("真实SQL合同解析失败: %v", err)
	}
	path := filepath.Join(t.TempDir(), "queries.json")
	if _, err := loadQueries(path); err == nil {
		t.Fatal("缺失SQL文件未失败")
	}
	for _, data := range []string{"{", "{}", strings.Repeat("x", maximumConfigBytes+1)} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadQueries(path); err == nil {
			t.Fatal("无效SQL合同未失败")
		}
	}
}
