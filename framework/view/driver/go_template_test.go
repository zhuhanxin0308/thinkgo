package driver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGoTemplateRejectsTraversalTemplateName 验证包含路径穿越片段的模板名会被拒绝，
// 而不是被清理后映射到视图目录内的其它模板。
func TestGoTemplateRejectsTraversalTemplateName(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "safe.html"), []byte(`safe`), 0o644); err != nil {
		t.Fatalf("写入测试模板失败: %v", err)
	}

	driver := NewGoTemplate()
	driver.Config(map[string]interface{}{
		"view_path":   dir,
		"view_suffix": "html",
	})

	if driver.Exists("../safe") {
		t.Fatal("包含 .. 的模板名应被拒绝，不应被清理后访问 safe.html")
	}
	if _, err := driver.Fetch("../safe", nil); err == nil || !strings.Contains(err.Error(), "非法模板名") {
		t.Fatalf("包含 .. 的模板名应返回非法模板错误，实际错误为 %v", err)
	}
	if !driver.Exists("safe") {
		t.Fatal("普通模板名仍应可访问")
	}
}

// TestGoTemplateConfigAcceptsNilMap 验证直接配置 nil 不会触发 panic，并会使用默认后缀。
func TestGoTemplateConfigAcceptsNilMap(t *testing.T) {
	driver := NewGoTemplate()
	driver.Config(nil)

	if _, ok := driver.config["view_suffix"]; !ok {
		t.Fatal("nil 配置应初始化默认 view_suffix")
	}
}
