package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLocalModuleDigestUsesSymlinkTargetAndExcludesGitMarker(t *testing.T) {
	dir := t.TempDir()
	for path, content := range map[string]string{
		"go.mod":    "module example.test/lib\n\ngo 1.20\n",
		"module.go": "package lib\nconst Message = \"original\"\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	before, err := digestLocalModule(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("普通 .git 文件", func(t *testing.T) {
		gitMarker := filepath.Join(dir, ".git")
		if err := os.WriteFile(gitMarker, []byte("gitdir: /local/git/worktrees/other\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		after, err := digestLocalModule(dir)
		if err != nil || before != after {
			t.Errorf("Git 元数据不应改变源码摘要: %q, %q, %v", before, after, err)
		}
	})
	t.Run("根目录链接", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "linked")
		if err := os.Symlink(dir, link); err != nil {
			if runtime.GOOS == "windows" {
				t.Skipf("Windows 未启用目录链接权限: %v", err)
			}
			t.Fatal(err)
		}
		actual, err := digestLocalModule(link)
		if err != nil || actual != before {
			t.Errorf("通过链接计算的摘要应匹配实际源码: %q, %v", actual, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "module.go"), []byte("package lib\nconst Message = \"changed\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		after, err := digestLocalModule(link)
		if err != nil || after == actual {
			t.Errorf("修改链接目标源码应改变摘要: %q, %v", after, err)
		}
	})
}

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
