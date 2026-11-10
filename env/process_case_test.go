package env

import (
	"os"
	"runtime"
	"testing"
)

// TestProcessEnvironmentCaseContract 验证所有读取入口遵循操作系统对规范键的查找语义。
func TestProcessEnvironmentCaseContract(t *testing.T) {
	const canonical = "THINKGO_CASE_CONTRACT"
	for _, spelling := range []string{"thinkgo_case_contract", "ThinkGo_Case_Contract", canonical} {
		for _, processValue := range []string{"process", ""} {
			t.Run(spelling+"/"+processValue, func(t *testing.T) {
				t.Setenv(canonical, "cleanup-marker")
				if err := os.Unsetenv(canonical); err != nil {
					t.Fatal(err)
				}
				t.Setenv(spelling, processValue)
				e := NewEnv()
				e.data[canonical] = "file"
				want := "file"
				if runtime.GOOS == "windows" || spelling == canonical {
					want = processValue
				}
				assertEnvironmentViews(t, e, canonical, want)
				if runtime.GOOS != "windows" && spelling != canonical {
					delete(e.data, canonical)
					if _, found := e.Snapshot().Lookup(canonical); found {
						t.Fatal("快照不应将非规范进程键提升为有效配置")
					}
					if _, found := e.All()[canonical]; found {
						t.Fatal("All 不应包含非规范进程键")
					}
				}
				t.Setenv(canonical, "canonical")
				assertEnvironmentViews(t, e, canonical, "canonical")
				frozen := e.Snapshot()
				t.Setenv(canonical, "updated")
				assertEnvironmentViews(t, e, canonical, "updated")
				assertEnvironmentViews(t, frozen, canonical, "canonical")
			})
		}
	}
}

func assertEnvironmentViews(t *testing.T, e *Env, key, want string) {
	t.Helper()
	for _, view := range []*Env{e, e.Snapshot()} {
		if got, found := view.Lookup(key); !found || got != want {
			t.Fatalf("Lookup=%q,%v，期望 %q", got, found, want)
		}
		if got := view.Get("thinkgo.case.contract"); got != want {
			t.Fatalf("Get=%q，期望 %q", got, want)
		}
		if got := view.All()[key]; got != want {
			t.Fatalf("All=%q，期望 %q", got, want)
		}
	}
}
