package driver

import (
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestGoTemplateCachedExecutionIsolation 验证普通执行、请求覆盖和缓存失效共享一致的模板版本。
func TestGoTemplateCachedExecutionIsolation(t *testing.T) {
	for _, cached := range []bool{false, true} {
		directory := t.TempDir()
		if err := os.WriteFile(filepath.Join(directory, "index.html"), []byte(`{{lang "hello"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		driver := NewGoTemplate()
		if err := driver.Config(map[string]interface{}{"view_path": directory, "cache": cached}); err != nil {
			t.Fatal(err)
		}
		if err := driver.SetFuncMap(map[string]interface{}{"lang": func(string) string { return "默认" }}); err != nil {
			t.Fatal(err)
		}
		var workers sync.WaitGroup
		const renderCount = 32
		for index := range renderCount {
			workers.Go(func() {
				expected := "默认"
				var overrides map[string]interface{}
				if index%2 == 0 {
					expected = "请求"
					overrides = map[string]interface{}{"lang": func(string) string { return "请求" }}
				}
				content, err := driver.FetchWithFuncMap("index", nil, overrides)
				if err != nil || content != expected {
					t.Errorf("请求函数串扰: cache=%v got=%q want=%q err=%v", cached, content, expected, err)
				}
			})
		}
		workers.Wait()
		if err := driver.SetFuncMap(map[string]interface{}{"lang": func(string) string { return "更新" }}); err != nil {
			t.Fatal(err)
		}
		if content, err := driver.Fetch("index", nil); err != nil || content != "更新" {
			t.Fatalf("函数更新后仍执行旧快照: content=%q err=%v", content, err)
		}
		if err := os.WriteFile(filepath.Join(directory, "index.html"), []byte(`新版：{{lang "hello"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := driver.Config(map[string]interface{}{"view_path": directory, "cache": cached}); err != nil {
			t.Fatal(err)
		}
		if content, err := driver.Fetch("index", nil); err != nil || content != "新版：更新" {
			t.Fatalf("配置更新后仍执行旧快照: content=%q err=%v", content, err)
		}
	}
}

// TestGoTemplateCachedExecutionAllocationBudget 约束小模板热路径，防止逐请求重建命名空间。
func TestGoTemplateCachedExecutionAllocationBudget(t *testing.T) {
	const maximumCachedRenderAllocations = 64
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "index.html"), []byte(`{{.Title}} {{.Body}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	driver := NewGoTemplate()
	if err := driver.Config(map[string]interface{}{"view_path": directory}); err != nil {
		t.Fatal(err)
	}
	data := map[string]interface{}{"Title": "标题", "Body": "内容"}
	allocations := testing.AllocsPerRun(100, func() {
		if err := driver.Display(io.Discard, "index", data); err != nil {
			t.Fatal(err)
		}
	})
	if allocations > maximumCachedRenderAllocations {
		t.Fatalf("缓存命中的分配超出预算: got=%v budget=%v", allocations, maximumCachedRenderAllocations)
	}
}
