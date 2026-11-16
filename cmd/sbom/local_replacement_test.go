package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalReplacementBOMUsesCurrentModuleSource(t *testing.T) {
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOSUMDB", "off")
	work := t.TempDir()
	root, library := filepath.Join(work, "app"), filepath.Join(work, "library")
	for _, dir := range []string{root, library} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for path, content := range map[string]string{
		filepath.Join(root, "go.mod"):    "module example.test/app\n\ngo 1.20\n\nrequire example.test/lib v1.2.3\nreplace example.test/lib => ../library\n",
		filepath.Join(library, "go.mod"): "module example.test/lib\n\ngo 1.20\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	build := func(output, source string) component {
		t.Helper()
		if err := os.WriteFile(filepath.Join(library, "library.go"), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
		output = filepath.Join(work, output)
		if err := generateAt(output, "example.test/app", "v1.0.0", root, "application"); err != nil {
			t.Fatal(err)
		}
		encoded, err := os.ReadFile(output)
		if err != nil {
			t.Fatal(err)
		}
		var result bom
		if err := json.Unmarshal(encoded, &result); err != nil || len(result.Components) != 1 || len(result.Dependencies) != 2 {
			t.Fatalf("本地替换清单无效: %+v, %v", result, err)
		}
		current := result.Components[0]
		rootDependency := result.Dependencies[0].DependsOn
		if current.Version != "" || current.PURL != "" || len(current.Hashes) != 1 ||
			current.Hashes[0].Algorithm != "SHA-256" || len(current.Hashes[0].Content) != 64 ||
			!strings.HasSuffix(current.BomRef, "sha256:"+current.Hashes[0].Content) ||
			len(rootDependency) != 1 || rootDependency[0] != current.BomRef ||
			result.Dependencies[1].Ref != current.BomRef {
			t.Errorf("本地源码身份错误或依赖图断裂: %+v, %+v", current, result.Dependencies)
		}
		for _, property := range current.Properties {
			if strings.Contains(property.Value, work) || filepath.IsAbs(property.Value) {
				t.Errorf("公开清单不应记录本地绝对路径: %+v", property)
			}
		}
		return current
	}
	const source = "package library\nconst Version = \"changed\"\n"
	initial := build("initial.json", source)
	unchanged := build("unchanged.json", source)
	changed := build("changed.json", source+"const Added = true\n")
	if initial.BomRef != unchanged.BomRef || initial.BomRef == changed.BomRef {
		t.Fatalf("清单引用应只在源码发生变化时改变: %q, %q, %q", initial.BomRef, unchanged.BomRef, changed.BomRef)
	}
}
