package env

import (
	"os"
	"testing"
)

// TestWindowsProcessUnicodeCaseContract 使用操作系统查询结果验证 Unicode 大小写语义。
func TestWindowsProcessUnicodeCaseContract(t *testing.T) {
	for _, suffix := range []string{"ı", "ſ", "σ", "ς", "é", "K", "a.b"} {
		t.Run(suffix, func(t *testing.T) {
			key := "THINKGO_UNICODE_CASE_" + suffix
			canonical := normalizeEnvName(key)
			t.Setenv(canonical, "cleanup-marker")
			if err := os.Unsetenv(canonical); err != nil {
				t.Fatal(err)
			}
			t.Setenv(key, "captured")
			want, found := os.LookupEnv(canonical)
			environment := NewEnv()
			frozen := environment.Snapshot()
			for _, view := range []*Env{environment, frozen} {
				if got, exists := view.Lookup(canonical); exists != found || got != want {
					t.Fatalf("Lookup(%q)=%q,%v，系统查询=%q,%v", canonical, got, exists, want, found)
				}
				if got, exists := view.All()[canonical]; exists != found || got != want {
					t.Fatalf("All[%q]=%q,%v，系统查询=%q,%v", canonical, got, exists, want, found)
				}
			}
			t.Setenv(key, "updated")
			if got, exists := frozen.Lookup(canonical); exists != found || got != want {
				t.Fatal("冻结的 Unicode 进程环境随后发生改变")
			}
		})
	}
}
