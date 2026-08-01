package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunValidatesArguments(t *testing.T) {
	var stderr strings.Builder
	if err := run(nil, &stderr); err == nil || !strings.Contains(err.Error(), "output") {
		t.Fatalf("缺少输出参数应失败，实际 %v", err)
	}
	if err := run([]string{"-output", "bom.json"}, &stderr); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("缺少版本参数应失败，实际 %v", err)
	}
	if err := run([]string{"-unknown"}, &stderr); err == nil {
		t.Fatal("未知参数应失败")
	}
}

func TestListModulesAndGenerateRealBOM(t *testing.T) {
	modules, err := listModules()
	if err != nil {
		t.Fatalf("读取真实模块清单失败: %v", err)
	}
	if len(modules) == 0 || modules[0].Path == "" {
		t.Fatalf("真实模块清单为空: %#v", modules)
	}
	output := filepath.Join(t.TempDir(), "real-bom.json")
	if err := generate(output, "thinkgo", "2.0.0"); err != nil {
		t.Fatalf("生成真实 SBOM 失败: %v", err)
	}
	content, err := os.ReadFile(output)
	if err != nil || !strings.Contains(string(content), "CycloneDX") || !strings.Contains(string(content), "thinkgo") {
		t.Fatalf("真实 SBOM 内容错误: %v", err)
	}
}

func TestListModulesReportsCommandErrors(t *testing.T) {
	previous := executeGoListCommand
	t.Cleanup(func() { executeGoListCommand = previous })
	executeGoListCommand = func(context.Context) ([]byte, error) {
		return nil, errors.New("go list unavailable")
	}
	if _, err := listModules(); err == nil || !strings.Contains(err.Error(), "执行 go list 失败") {
		t.Fatalf("无输出命令错误未保留上下文: %v", err)
	}
	executeGoListCommand = func(context.Context) ([]byte, error) {
		return []byte("proxy unavailable"), errors.New("go list unavailable")
	}
	if _, err := listModules(); err == nil || !strings.Contains(err.Error(), "proxy unavailable") {
		t.Fatalf("命令 stderr 未保留: %v", err)
	}
}

func TestBuildBOMSortsAndNormalizesModules(t *testing.T) {
	result, err := buildBOM("thinkgo", "2.0.0", []moduleInfo{
		{Path: "z.example/library", Version: "v1.0.0", Sum: "h1:module", GoModSum: "h1:gomod"},
		{Path: "thinkgo", Main: true},
		{Path: "a.example/library", Version: "v2.0.0", Replace: &moduleInfo{Path: "local/library", Version: "v2.1.0"}},
	})
	if err != nil {
		t.Fatalf("构建 SBOM 失败: %v", err)
	}
	if result.BomFormat != cycloneDXFormat || result.SpecVersion != cycloneDXVersion || result.Version != 1 {
		t.Fatalf("CycloneDX 元数据错误: %#v", result)
	}
	if result.Metadata.Component.Type != "application" || result.Metadata.Component.Version != "2.0.0" {
		t.Fatalf("应用元数据错误: %#v", result.Metadata.Component)
	}
	if len(result.Components) != 3 {
		t.Fatalf("组件数量错误: %d", len(result.Components))
	}
	if result.Components[0].PURL != "pkg:golang/a.example/library@v2.1.0" {
		t.Fatalf("替换模块未使用实际版本: %#v", result.Components[0])
	}
	if result.Components[1].PURL != "pkg:golang/thinkgo" || result.Components[2].PURL != "pkg:golang/z.example/library@v1.0.0" {
		t.Fatalf("组件排序错误: %#v", result.Components)
	}
	if len(result.Components[2].Properties) != 2 || result.Components[2].Properties[0].Name != "go.module.sum" || result.Components[2].Properties[1].Name != "go.mod.sum" {
		t.Fatalf("模块完整性属性缺失: %#v", result.Components[2].Properties)
	}
	encoded, err := json.Marshal(result)
	if err != nil || !strings.Contains(string(encoded), "CycloneDX") {
		t.Fatalf("SBOM JSON 编码失败: %v %s", err, encoded)
	}
}

func TestModuleComponentRejectsInvalidReplacement(t *testing.T) {
	if _, err := moduleComponent(moduleInfo{Path: "example/library", Replace: &moduleInfo{}}); err == nil {
		t.Fatal("替换模块路径为空时应失败")
	}
	if _, err := buildBOM("", "2.0.0", nil); err == nil {
		t.Fatal("模块名称为空时应失败")
	}
}

func TestWriteJSONCreatesParentAndDoesNotOverwrite(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "nested", "bom.json")
	if err := writeJSON(output, map[string]string{"name": "thinkgo"}); err != nil {
		t.Fatalf("写入 SBOM 失败: %v", err)
	}
	content, err := os.ReadFile(output)
	if err != nil || !strings.Contains(string(content), "thinkgo") {
		t.Fatalf("SBOM 内容错误: %v %s", err, content)
	}
	if err := writeJSON(output, map[string]string{"name": "again"}); err == nil {
		t.Fatal("写入已存在文件时应失败")
	} else if !strings.Contains(err.Error(), "已存在") {
		t.Fatalf("错误未保留文件存在语义: %v", err)
	}
}

func TestWriteJSONRejectsEmptyPath(t *testing.T) {
	if err := writeJSON("", map[string]string{"name": "thinkgo"}); err == nil {
		t.Fatal("空输出路径应失败")
	}
}
