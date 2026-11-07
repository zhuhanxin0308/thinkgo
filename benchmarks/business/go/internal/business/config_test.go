package business

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func stringJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestDatasetRejectsUnknownAndInvalidLimits(t *testing.T) {
	data, err := os.ReadFile("../../../contracts/dataset.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"未知配置":    strings.Replace(string(data), `"schema_version": 1`, `"schema_version": 1, "unrecognized": 5`, 1),
		"负连接数":    strings.Replace(string(data), `"database_connection_budget": 24`, `"database_connection_budget": -1`, 1),
		"错误比例":    strings.Replace(string(data), `"catalog": 40`, `"catalog": 41`, 1),
		"不完整JSON": "{",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "dataset.json")
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadDataset(path); err == nil {
				t.Fatal("错误数据规模配置被接受")
			}
		})
	}
}

func TestLoadConfigRequiresExplicitLocalDependencies(t *testing.T) {
	t.Setenv("BENCH_CONTRACT_DIR", "../../../contracts")
	t.Setenv("BENCH_DATABASE_URL", "postgres://benchmark@127.0.0.1:15432/benchmark_run?sslmode=disable")
	t.Setenv("BENCH_REDIS_ADDR", "127.0.0.1:16379")
	t.Setenv("BENCH_SECRET", "test-only-signing-key-with-at-least-32-bytes")
	t.Setenv("BENCH_NAMESPACE", "bench:test")
	t.Setenv("BENCH_ADDR", "127.0.0.1:18080")
	if _, err := LoadConfig(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BENCH_SECRET", "short")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("过短签名密钥被接受")
	}
	t.Setenv("BENCH_SECRET", "test-only-signing-key-with-at-least-32-bytes")
	t.Setenv("BENCH_DATABASE_URL", "postgres://benchmark@db.example.com/benchmark_run")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("本地基准不能误连接外部数据库")
	}
}
