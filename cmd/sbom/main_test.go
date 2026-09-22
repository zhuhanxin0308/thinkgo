package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
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

// TestRunGeneratesFrameworkLibraryBOM 验证发布命令从 framework 子模块读取
// 依赖，并把主组件标记为带 Apache-2.0 许可证的 library。
func TestRunGeneratesFrameworkLibraryBOM(t *testing.T) {
	output := filepath.Join(t.TempDir(), "framework-bom.json")
	var stderr strings.Builder
	err := run([]string{
		"-directory", "../../framework",
		"-component-type", "library",
		"-module", "github.com/zhuhanxin0308/thinkgo/framework",
		"-version", "1.0.0",
		"-output", output,
	}, &stderr)
	if err != nil {
		t.Fatalf("生成框架 SBOM 失败: %v", err)
	}
	content, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("读取框架 SBOM 失败: %v", err)
	}
	var result bom
	if err := json.Unmarshal(content, &result); err != nil {
		t.Fatalf("解析框架 SBOM 失败: %v", err)
	}
	if result.Metadata.Component.Type != "library" || result.Metadata.Component.Name != "github.com/zhuhanxin0308/thinkgo/framework" {
		t.Fatalf("框架主组件身份错误: %#v", result.Metadata.Component)
	}
	if len(result.Metadata.Component.Licenses) != 1 || result.Metadata.Component.Licenses[0].License.ID != "Apache-2.0" {
		t.Fatalf("框架主组件许可证缺失: %#v", result.Metadata.Component.Licenses)
	}
	if result.SerialNumber == "" || result.Metadata.Timestamp == "" || len(result.Dependencies) == 0 {
		t.Fatalf("框架 SBOM 缺少序列号、时间或依赖关系: %#v", result)
	}
	if !regexp.MustCompile(`^urn:uuid:[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(result.SerialNumber) {
		t.Fatalf("框架 SBOM 序列号不是 RFC 4122 UUID: %q", result.SerialNumber)
	}
	knownReferences := map[string]bool{result.Metadata.Component.BomRef: true}
	for _, current := range result.Components {
		knownReferences[current.BomRef] = true
	}
	hasComponentDependency := false
	for _, current := range result.Dependencies {
		if !knownReferences[current.Ref] {
			t.Fatalf("依赖图包含未知组件引用: %q", current.Ref)
		}
		if current.Ref != result.Metadata.Component.BomRef && len(current.DependsOn) > 0 {
			hasComponentDependency = true
		}
		for _, target := range current.DependsOn {
			if !knownReferences[target] {
				t.Fatalf("依赖图指向未知组件: %q", target)
			}
		}
	}
	if !hasComponentDependency {
		t.Fatal("真实框架 SBOM 丢失了组件之间的传递依赖")
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
	executeGoListCommand = func(context.Context, string) ([]byte, error) {
		return nil, errors.New("go list unavailable")
	}
	if _, err := listModules(); err == nil || !strings.Contains(err.Error(), "执行 go list 失败") {
		t.Fatalf("无输出命令错误未保留上下文: %v", err)
	}
	executeGoListCommand = func(context.Context, string) ([]byte, error) {
		return []byte("proxy unavailable"), errors.New("go list unavailable")
	}
	if _, err := listModules(); err == nil || !strings.Contains(err.Error(), "proxy unavailable") {
		t.Fatalf("命令 stderr 未保留: %v", err)
	}
}

func TestListModuleGraphParsesAndRejectsMalformedLines(t *testing.T) {
	previous := executeGoModGraphCommand
	t.Cleanup(func() { executeGoModGraphCommand = previous })
	executeGoModGraphCommand = func(context.Context, string) ([]byte, error) {
		return []byte("example.com/root example.com/direct@v1.0.0\nexample.com/direct@v1.0.0 example.com/transitive@v2.0.0\n"), nil
	}
	edges, err := listModuleGraphAt(".")
	if err != nil {
		t.Fatalf("解析模块依赖图失败: %v", err)
	}
	if len(edges) != 2 || edges[0].Parent != "example.com/root" || edges[1].Child != "example.com/transitive@v2.0.0" {
		t.Fatalf("模块依赖图解析错误: %#v", edges)
	}
	executeGoModGraphCommand = func(context.Context, string) ([]byte, error) {
		return []byte("malformed-line"), nil
	}
	if _, err := listModuleGraphAt("."); err == nil || !strings.Contains(err.Error(), "格式错误") {
		t.Fatalf("畸形模块依赖图未被拒绝: %v", err)
	}
}

func TestBuildBOMSortsAndPreservesDependencyGraph(t *testing.T) {
	result, err := buildBOM("thinkgo", "2.0.0", []moduleInfo{
		{Path: "z.example/library", Version: "v1.0.0", Sum: "h1:module", GoModSum: "h1:gomod"},
		{Path: "thinkgo", Main: true},
		{Path: "a.example/library", Version: "v2.0.0", Replace: &moduleInfo{Path: "local/library", Version: "v2.1.0"}},
	}, []moduleGraphEdge{
		{Parent: "thinkgo", Child: "a.example/library@v2.0.0"},
		{Parent: "a.example/library@v2.0.0", Child: "z.example/library@v1.0.0"},
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
	if len(result.Components) != 2 {
		t.Fatalf("组件数量错误: %d", len(result.Components))
	}
	if result.Components[0].PURL != "pkg:golang/a.example/library@v2.1.0" {
		t.Fatalf("替换模块未使用实际版本: %#v", result.Components[0])
	}
	if result.Components[1].PURL != "pkg:golang/z.example/library@v1.0.0" {
		t.Fatalf("组件排序错误: %#v", result.Components)
	}
	if len(result.Components[1].Properties) != 2 || result.Components[1].Properties[0].Name != "go.module.sum" || result.Components[1].Properties[1].Name != "go.mod.sum" {
		t.Fatalf("模块完整性属性缺失: %#v", result.Components[1].Properties)
	}
	if len(result.Dependencies) != 3 || len(result.Dependencies[0].DependsOn) != 1 {
		t.Fatalf("依赖关系清单不完整: %#v", result.Dependencies)
	}
	if result.Dependencies[0].DependsOn[0] != result.Components[0].BomRef {
		t.Fatalf("根组件错误地直接依赖传递组件: %#v", result.Dependencies)
	}
	if len(result.Dependencies[1].DependsOn) != 1 || result.Dependencies[1].DependsOn[0] != result.Components[1].BomRef {
		t.Fatalf("组件之间的传递依赖未保留: %#v", result.Dependencies)
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
	if _, err := buildBOM("", "2.0.0", nil, nil); err == nil {
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
