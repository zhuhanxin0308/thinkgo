package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestConfigGetCachesResolvedDotLookup 验证点路径查询会写入缓存，避免每次重复拆分和逐层遍历。
func TestConfigGetCachesResolvedDotLookup(t *testing.T) {
	cfg := NewConfig()
	cfg.Set("app.server.port", 8080)

	if got := cfg.Get("app.server.port"); got != 8080 {
		t.Fatalf("点路径配置读取结果错误，期望 8080，实际为 %#v", got)
	}

	cacheField := reflect.ValueOf(cfg).Elem().FieldByName("lookupCache")
	if !cacheField.IsValid() {
		t.Fatal("Config 应维护点路径查询缓存")
	}
	if cacheField.Len() != 1 {
		t.Fatalf("首次点路径查询后应写入 1 条缓存，实际为 %d", cacheField.Len())
	}
	if !cacheField.MapIndex(reflect.ValueOf("app.server.port")).IsValid() {
		t.Fatal("点路径查询后应缓存 app.server.port 的解析结果")
	}
}

// TestConfigSetInvalidatesLookupCache 验证配置更新后会清空旧缓存，避免继续返回陈旧值。
func TestConfigSetInvalidatesLookupCache(t *testing.T) {
	cfg := NewConfig()
	cfg.Set("app.server.port", 8080)
	_ = cfg.Get("app.server.port")

	cfg.Set("app.server.port", 9090)

	cacheField := reflect.ValueOf(cfg).Elem().FieldByName("lookupCache")
	if !cacheField.IsValid() {
		t.Fatal("Config 应维护点路径查询缓存")
	}
	if cacheField.Len() != 0 {
		t.Fatalf("Set 后应清空旧查询缓存，实际残留 %d 条", cacheField.Len())
	}

	if got := cfg.Get("app.server.port"); got != 9090 {
		t.Fatalf("配置更新后应返回新值 9090，实际为 %#v", got)
	}
}

// TestConfigLoadAndMerge 验证命名空间加载、根配置加载和已有非 map 值替换。
func TestConfigLoadAndMerge(t *testing.T) {
	dir := t.TempDir()
	appFile := filepath.Join(dir, "app.json")
	rootFile := filepath.Join(dir, "root.json")
	if err := os.WriteFile(appFile, []byte(`{"server":{"port":8080},"debug":true}`), 0o600); err != nil {
		t.Fatalf("写入配置文件失败: %v", err)
	}
	if err := os.WriteFile(rootFile, []byte(`{"APP_NAME":"thinkgo"}`), 0o600); err != nil {
		t.Fatalf("写入根配置文件失败: %v", err)
	}

	cfg := NewConfig()
	cfg.Set("app", "旧值")
	if err := cfg.Load(appFile, "APP"); err != nil {
		t.Fatalf("加载命名空间配置失败: %v", err)
	}
	if got := cfg.GetInt("app.server.port"); got != 8080 {
		t.Fatalf("JSON 数值应转换为整数 8080，实际为 %d", got)
	}
	if !cfg.GetBool("app.debug") {
		t.Fatal("JSON 布尔配置加载失败")
	}
	if err := cfg.Load(rootFile, ""); err != nil {
		t.Fatalf("加载根配置失败: %v", err)
	}
	if got := cfg.GetString("app_name"); got != "thinkgo" {
		t.Fatalf("根配置键应统一小写，实际为 %q", got)
	}
}

// TestConfigTypedGettersAndMissingValues 验证类型读取、默认值、存在性和缺失路径缓存。
func TestConfigTypedGettersAndMissingValues(t *testing.T) {
	cfg := NewConfig()
	cfg.Set("types", map[string]interface{}{
		"string": "value",
		"bool":   true,
		"one":    "1",
		"int":    int(3),
		"int64":  int64(4),
		"float":  float64(5),
		"wrong":  struct{}{},
	})

	if got := cfg.GetString("types.string", "fallback"); got != "value" {
		t.Fatalf("字符串读取错误: %q", got)
	}
	if got := cfg.GetString("types.wrong", "fallback"); got != "fallback" {
		t.Fatalf("错误类型应返回字符串默认值，实际为 %q", got)
	}
	if !cfg.GetBool("types.bool") || !cfg.GetBool("types.one") {
		t.Fatal("布尔值和字符串 1 均应解析为 true")
	}
	if cfg.GetBool("types.wrong", true) != true || cfg.GetBool("types.missing", true) != true {
		t.Fatal("错误类型或缺失布尔配置应返回默认值")
	}
	for key, want := range map[string]int{"int": 3, "int64": 4, "float": 5} {
		if got := cfg.GetInt("types." + key); got != want {
			t.Fatalf("整数类型 %s 读取错误，want=%d got=%d", key, want, got)
		}
	}
	if got := cfg.GetInt("types.wrong", 9); got != 9 {
		t.Fatalf("错误整数类型应返回默认值，实际为 %d", got)
	}
	if !cfg.Has("types.string") || cfg.Has("types.absent") {
		t.Fatal("Has 未正确区分存在与缺失配置")
	}
	if got := cfg.Get("types.deep.absent", "fallback"); got != "fallback" {
		t.Fatalf("缺失点路径应返回默认值，实际为 %#v", got)
	}
	if got := cfg.Get("types.deep.absent", "cached-fallback"); got != "cached-fallback" {
		t.Fatalf("缓存的缺失点路径应使用本次默认值，实际为 %#v", got)
	}
	if got := cfg.GetMap("types.string"); got == nil || len(got) != 0 {
		t.Fatalf("非 map 配置应返回非 nil 空 map，实际为 %#v", got)
	}
}

// TestConfigCopiesTypedCollections 验证常见强类型 map 和切片同样采用快照语义。
func TestConfigCopiesTypedCollections(t *testing.T) {
	cfg := NewConfig()
	cfg.Set("collections", map[string]interface{}{
		"labels":      map[string]string{"role": "api"},
		"ints":        []int{1},
		"int64s":      []int64{2},
		"floats":      []float64{3},
		"booleans":    []bool{true},
		"typed_map":   map[string][]string{"hosts": {"a.example"}},
		"typed_slice": []map[string]string{{"name": "primary"}},
	})

	first := cfg.GetMap("collections")
	first["labels"].(map[string]string)["role"] = "mutated"
	first["ints"].([]int)[0] = 9
	first["int64s"].([]int64)[0] = 9
	first["floats"].([]float64)[0] = 9
	first["booleans"].([]bool)[0] = false
	first["typed_map"].(map[string][]string)["hosts"][0] = "mutated.example"
	first["typed_slice"].([]map[string]string)[0]["name"] = "mutated"

	second := cfg.GetMap("collections")
	if second["labels"].(map[string]string)["role"] != "api" ||
		second["ints"].([]int)[0] != 1 || second["int64s"].([]int64)[0] != 2 ||
		second["floats"].([]float64)[0] != 3 || !second["booleans"].([]bool)[0] ||
		second["typed_map"].(map[string][]string)["hosts"][0] != "a.example" ||
		second["typed_slice"].([]map[string]string)[0]["name"] != "primary" {
		t.Fatalf("强类型集合快照被外部修改污染：%#v", second)
	}
}

// TestSetReplacesScalarIntermediate 验证点路径写入会安全替换无法继续下钻的标量节点。
func TestSetReplacesScalarIntermediate(t *testing.T) {
	cfg := NewConfig()
	cfg.Set("app", "scalar")
	cfg.Set("app.server.port", 9000)
	if got := cfg.GetInt("app.server.port"); got != 9000 {
		t.Fatalf("标量中间节点替换失败，实际端口为 %d", got)
	}
}
