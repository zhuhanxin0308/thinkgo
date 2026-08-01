package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestConfigLoadUsesThinkPHPShallowMerge 验证命名空间合并只合并第一层，嵌套 map 会整体替换。
func TestConfigLoadUsesThinkPHPShallowMerge(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.json")
	second := filepath.Join(dir, "second.json")
	if err := os.WriteFile(first, []byte(`{"server":{"host":"127.0.0.1","tls":{"enable":true}},"debug":true}`), 0o600); err != nil {
		t.Fatalf("写入第一份配置失败: %v", err)
	}
	if err := os.WriteFile(second, []byte(`{"server":{"port":8080},"debug":false}`), 0o600); err != nil {
		t.Fatalf("写入第二份配置失败: %v", err)
	}

	cfg := NewConfig()
	if err := cfg.Load(first, "app"); err != nil {
		t.Fatalf("加载第一份配置失败: %v", err)
	}
	if err := cfg.Load(second, "APP"); err != nil {
		t.Fatalf("加载第二份配置失败: %v", err)
	}

	if got := cfg.GetInt("app.server.port"); got != 8080 {
		t.Fatalf("后加载的第一层键没有生效: %d", got)
	}
	if cfg.Has("app.server.host") || cfg.Has("app.server.tls.enable") {
		t.Fatal("嵌套 map 不应按深度合并")
	}
	if cfg.GetBool("app.debug", true) {
		t.Fatal("后加载的标量键没有覆盖旧值")
	}
}

// TestConfigLoadRejectsDuplicateCaseInsensitiveKeys 验证大小写不同的重复键不会被静默覆盖。
func TestConfigLoadRejectsDuplicateCaseInsensitiveKeys(t *testing.T) {
	file := filepath.Join(t.TempDir(), "duplicate.json")
	if err := os.WriteFile(file, []byte(`{"Port":8080,"port":9090}`), 0o600); err != nil {
		t.Fatalf("写入重复键配置失败: %v", err)
	}

	err := NewConfig().Load(file, "app")
	if !errors.Is(err, ErrConfigDuplicateKey) {
		t.Fatalf("重复键应返回 ErrConfigDuplicateKey，实际为 %v", err)
	}
}

// TestConfigLoadAllIsAtomic 验证批量加载失败时不会提交前面已经解析成功的文件。
func TestConfigLoadAllIsAtomic(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "good.json"), []byte(`{"enabled":true}`), 0o600); err != nil {
		t.Fatalf("写入有效配置失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte(`{"enabled":`), 0o600); err != nil {
		t.Fatalf("写入损坏配置失败: %v", err)
	}

	cfg := NewConfig()
	cfg.Set("stable.value", "before")
	if err := cfg.LoadAll(dir); err == nil {
		t.Fatal("批量加载损坏配置应返回错误")
	}
	if cfg.Has("good.enabled") {
		t.Fatal("批量加载失败后不应残留已解析配置")
	}
	if got := cfg.GetString("stable.value"); got != "before" {
		t.Fatalf("批量加载失败不应影响已有配置: %q", got)
	}
}

// TestConfigHasRecognizesExplicitNil 验证显式 nil 配置仍然具有存在性。
func TestConfigHasRecognizesExplicitNil(t *testing.T) {
	cfg := NewConfig()
	cfg.Set("console.user", nil)
	if !cfg.Has("console.user") {
		t.Fatal("显式 nil 配置应被 Has 识别为存在")
	}
}

// TestConfigLoadRejectsTooDeepJSON 验证过深 JSON 会在反序列化前被拒绝。
func TestConfigLoadRejectsTooDeepJSON(t *testing.T) {
	depth := maxConfigJSONDepth + 2
	content := strings.Repeat(`{"nested":`, depth) + "null" + strings.Repeat("}", depth)
	file := filepath.Join(t.TempDir(), "deep.json")
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatalf("写入深层配置失败: %v", err)
	}

	if err := NewConfig().Load(file, "app"); !errors.Is(err, ErrConfigJSONTooDeep) {
		t.Fatalf("过深 JSON 应返回 ErrConfigJSONTooDeep，实际为 %v", err)
	}
}

// TestConfigLoadAllRejectsDuplicateNamespaces 验证批量加载不会让大小写不同的文件覆盖同一命名空间。
func TestConfigLoadAllRejectsDuplicateNamespaces(t *testing.T) {
	dir := t.TempDir()
	child := filepath.Join(dir, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatalf("创建子目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.json"), []byte(`{"name":"root"}`), 0o600); err != nil {
		t.Fatalf("写入根配置失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(child, "APP.json"), []byte(`{"name":"child"}`), 0o600); err != nil {
		t.Fatalf("写入子目录配置失败: %v", err)
	}

	if err := NewConfig().LoadAll(dir); !errors.Is(err, ErrConfigDuplicateNamespace) {
		t.Fatalf("重复命名空间应返回 ErrConfigDuplicateNamespace，实际为 %v", err)
	}
}

// TestConfigStrictTypedGettersRejectInvalidValues 验证严格类型读取不会把非法值静默转换。
func TestConfigStrictTypedGettersRejectInvalidValues(t *testing.T) {
	cfg := NewConfig()
	cfg.Set("values.bool", "maybe")
	cfg.Set("values.number", 1.5)

	if _, err := cfg.GetBoolStrict("values.bool"); err == nil {
		t.Fatal("非法布尔字符串应返回错误")
	}
	if _, err := cfg.GetIntStrict("values.number"); err == nil {
		t.Fatal("小数配置不应被截断为整数")
	}
}

// TestConfigGetIntUsesDefaultWhenMissing 验证缺失整数配置会使用调用方提供的默认值。
func TestConfigGetIntUsesDefaultWhenMissing(t *testing.T) {
	cfg := NewConfig()
	if got := cfg.GetInt("server.port", 8080); got != 8080 {
		t.Fatalf("缺失整数配置应返回默认值，实际为 %d", got)
	}
}

// TestConfigSetCanonicalizesNestedMapKeys 验证代码写入的嵌套 map 与 JSON 配置保持大小写不敏感。
func TestConfigSetCanonicalizesNestedMapKeys(t *testing.T) {
	cfg := NewConfig()
	if err := cfg.Set("APP", map[string]interface{}{
		"Server": map[string]interface{}{"PORT": 8080},
	}); err != nil {
		t.Fatalf("写入合法配置失败: %v", err)
	}
	if got := cfg.GetInt("app.server.port"); got != 8080 {
		t.Fatalf("嵌套配置键未统一小写，实际端口为 %d", got)
	}
}

// TestConfigSetRejectsInvalidPath 验证非法点路径会向调用方返回明确错误。
func TestConfigSetRejectsInvalidPath(t *testing.T) {
	cfg := NewConfig()
	if err := cfg.Set("app..server", true); !errors.Is(err, ErrConfigInvalidPath) {
		t.Fatalf("非法配置路径应返回 ErrConfigInvalidPath，实际为 %v", err)
	}
}

// TestConfigRejectsDottedNamespace 验证命名空间不会与点路径语义冲突。
func TestConfigRejectsDottedNamespace(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "source.json")
	if err := os.WriteFile(file, []byte(`{"value":1}`), 0o600); err != nil {
		t.Fatalf("写入测试配置失败: %v", err)
	}
	if err := NewConfig().Load(file, "foo.bar"); !errors.Is(err, ErrConfigInvalidNamespace) {
		t.Fatalf("带点命名空间应返回 ErrConfigInvalidNamespace，实际为 %v", err)
	}

	batchDir := filepath.Join(dir, "batch")
	if err := os.Mkdir(batchDir, 0o700); err != nil {
		t.Fatalf("创建批量配置目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(batchDir, "foo.bar.json"), []byte(`{"value":1}`), 0o600); err != nil {
		t.Fatalf("写入带点配置文件失败: %v", err)
	}
	if err := NewConfig().LoadAll(batchDir); !errors.Is(err, ErrConfigInvalidNamespace) {
		t.Fatalf("带点配置文件名应返回 ErrConfigInvalidNamespace，实际为 %v", err)
	}
}

// TestConfigPreservesJSONIntegerLexeme 验证大整数不会在通用配置读取中经过 float64 舍入。
func TestConfigPreservesJSONIntegerLexeme(t *testing.T) {
	file := filepath.Join(t.TempDir(), "large.json")
	if err := os.WriteFile(file, []byte(`{"large":9007199254740993}`), 0o600); err != nil {
		t.Fatalf("写入大整数配置失败: %v", err)
	}
	cfg := NewConfig()
	if err := cfg.Load(file, ""); err != nil {
		t.Fatalf("加载大整数配置失败: %v", err)
	}
	value, ok := cfg.Get("large").(json.Number)
	if !ok || value.String() != "9007199254740993" {
		t.Fatalf("大整数应保留 JSON 数字字面量，实际为 %#v", cfg.Get("large"))
	}
}

// TestConfigZeroValueCanSet 验证 Config 零值也能安全写入配置。
func TestConfigZeroValueCanSet(t *testing.T) {
	var cfg Config
	if err := cfg.Set("server.port", 8080); err != nil {
		t.Fatalf("Config 零值写入失败: %v", err)
	}
	if got := cfg.GetInt("server.port"); got != 8080 {
		t.Fatalf("Config 零值读取结果错误，实际为 %d", got)
	}
}

// TestConfigLoadRejectsUnsafeFilesAndJSONShapes 验证文件类型、文件大小和 JSON 根结构均经过严格校验。
func TestConfigLoadRejectsUnsafeFilesAndJSONShapes(t *testing.T) {
	t.Run("directory", func(t *testing.T) {
		if err := NewConfig().Load(t.TempDir(), "app"); err == nil {
			t.Fatal("配置路径为目录时必须返回错误")
		}
	})

	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target.json")
		link := filepath.Join(dir, "link.json")
		if err := os.WriteFile(target, []byte(`{"value":true}`), 0o600); err != nil {
			t.Fatalf("写入符号链接目标失败: %v", err)
		}
		if err := os.Symlink(target, link); err != nil {
			if runtime.GOOS == "windows" {
				t.Skipf("当前 Windows 环境不允许创建符号链接: %v", err)
			}
			t.Fatalf("创建配置符号链接失败: %v", err)
		}
		if err := NewConfig().Load(link, "app"); !errors.Is(err, ErrConfigSymlinkNotAllowed) {
			t.Fatalf("配置符号链接应返回 ErrConfigSymlinkNotAllowed，实际为 %v", err)
		}
	})

	t.Run("oversized", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "large.json")
		if err := os.WriteFile(file, []byte(strings.Repeat("x", int(maxConfigFileBytes)+1)), 0o600); err != nil {
			t.Fatalf("写入超大配置失败: %v", err)
		}
		if err := NewConfig().Load(file, "app"); !errors.Is(err, ErrConfigFileTooLarge) {
			t.Fatalf("超大配置应返回 ErrConfigFileTooLarge，实际为 %v", err)
		}
	})

	t.Run("trailing data", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "trailing.json")
		if err := os.WriteFile(file, []byte(`{"value":1}{"other":2}`), 0o600); err != nil {
			t.Fatalf("写入尾随数据配置失败: %v", err)
		}
		if err := NewConfig().Load(file, "app"); err == nil || !strings.Contains(err.Error(), "trailing data") {
			t.Fatalf("JSON 尾随数据应返回明确错误，实际为 %v", err)
		}
	})

	t.Run("root and array", func(t *testing.T) {
		nullFile := filepath.Join(t.TempDir(), "null.json")
		if err := os.WriteFile(nullFile, []byte("null"), 0o600); err != nil {
			t.Fatalf("写入 null 配置失败: %v", err)
		}
		if err := NewConfig().Load(nullFile, "app"); err == nil || !strings.Contains(err.Error(), "root must be an object") {
			t.Fatalf("JSON 根节点为 null 时应返回根对象错误，实际为 %v", err)
		}

		arrayFile := filepath.Join(t.TempDir(), "array.json")
		if err := os.WriteFile(arrayFile, []byte(`{"items":[{"Port":8080},true]}`), 0o600); err != nil {
			t.Fatalf("写入数组配置失败: %v", err)
		}
		cfg := NewConfig()
		if err := cfg.Load(arrayFile, "app"); err != nil {
			t.Fatalf("含数组的合法配置不应失败: %v", err)
		}
		items, ok := cfg.Get("app.items").([]interface{})
		if !ok || len(items) != 2 {
			t.Fatalf("数组配置读取错误: %#v", cfg.Get("app.items"))
		}
		if got, ok := items[0].(map[string]interface{})["port"].(json.Number); !ok || got.String() != "8080" {
			t.Fatalf("数组中的对象键和值未规范化: %#v", items[0])
		}
	})
}

// TestConfigSetRejectsDuplicateAndDeepInput 验证动态写入不会接受大小写重复键或过深集合。
func TestConfigSetRejectsDuplicateAndDeepInput(t *testing.T) {
	cfg := NewConfig()
	if err := cfg.Set("settings", map[string]interface{}{"Port": 8080, "port": 9090}); !errors.Is(err, ErrConfigDuplicateKey) {
		t.Fatalf("动态 map 大小写重复键应返回 ErrConfigDuplicateKey，实际为 %v", err)
	}
	if cfg.Has("settings") {
		t.Fatal("动态 map 校验失败后不得提交部分配置")
	}

	deep := interface{}("leaf")
	for index := 0; index <= maxConfigJSONDepth+1; index++ {
		deep = map[string]interface{}{"nested": deep}
	}
	if err := cfg.Set("deep", deep); !errors.Is(err, ErrConfigJSONTooDeep) {
		t.Fatalf("动态集合过深应返回 ErrConfigJSONTooDeep，实际为 %v", err)
	}
}

// TestConfigGetIntRejectsInvalidJSONNumbers 验证 JSON 小数和超范围数字不会被当作合法整数。
func TestConfigGetIntRejectsInvalidJSONNumbers(t *testing.T) {
	file := filepath.Join(t.TempDir(), "numbers.json")
	content := []byte(`{"fraction":1.5,"overflow":9223372036854775808}`)
	if err := os.WriteFile(file, content, 0o600); err != nil {
		t.Fatalf("写入数字配置失败: %v", err)
	}
	cfg := NewConfig()
	if err := cfg.Load(file, ""); err != nil {
		t.Fatalf("加载数字配置失败: %v", err)
	}
	if got := cfg.GetInt("fraction", 8080); got != 8080 {
		t.Fatalf("小数配置应返回默认值，实际为 %d", got)
	}
	if got := cfg.GetInt("overflow", 8080); got != 8080 {
		t.Fatalf("超范围配置应返回默认值，实际为 %d", got)
	}
}
