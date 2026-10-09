package command

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/mod/modfile"
)

// TestDiscoveryModuleFixtureTracksFrameworkGoRequirement uses synthetic module
// declarations to exercise version drift without requiring another Go toolchain.
func TestDiscoveryModuleFixtureTracksFrameworkGoRequirement(t *testing.T) {
	for _, requirement := range []string{"1.26.5", "1.26.9", "1.27.0"} {
		t.Run(requirement, func(t *testing.T) {
			frameworkRoot := t.TempDir()
			moduleSource := "module github.com/zhuhanxin0308/thinkgo/v3\n\ngo " + requirement + "\n"
			writeDiscoveryFixture(t, frameworkRoot, "go.mod", moduleSource)
			writeDiscoveryFixture(t, frameworkRoot, "go.sum", "")
			commandDir := filepath.Join(frameworkRoot, "console", "command")
			if err := os.MkdirAll(commandDir, 0o755); err != nil {
				t.Fatal(err)
			}
			t.Chdir(commandDir)

			project := t.TempDir()
			writeDiscoveryModuleFixture(t, project, "example.com/discovery-version")
			content, err := os.ReadFile(filepath.Join(project, "go.mod"))
			if err != nil {
				t.Fatal(err)
			}
			generated, err := modfile.Parse("go.mod", content, nil)
			if err != nil {
				t.Fatal(err)
			}
			if generated.Go == nil || generated.Go.Version != requirement {
				t.Fatalf("discovery module must use framework Go requirement %s, got:\n%s", requirement, content)
			}
			if generated.Module == nil || generated.Module.Mod.Path != "example.com/discovery-version" {
				t.Fatalf("fixture changed the downstream module path: %s", content)
			}
			if len(generated.Require) != 1 || generated.Require[0].Mod.Path != "github.com/zhuhanxin0308/thinkgo/v3" || generated.Require[0].Mod.Version != "v3.0.0" {
				t.Fatalf("fixture changed the framework dependency: %s", content)
			}
			if len(generated.Replace) != 1 || generated.Replace[0].Old.Path != "github.com/zhuhanxin0308/thinkgo/v3" {
				t.Fatalf("fixture no longer resolves the exact local framework: %s", content)
			}
			rootInfo, err := os.Stat(frameworkRoot)
			if err != nil {
				t.Fatal(err)
			}
			replacementInfo, err := os.Stat(generated.Replace[0].New.Path)
			if err != nil || !os.SameFile(rootInfo, replacementInfo) {
				t.Fatalf("replacement does not identify the local framework: %s, err=%v", content, err)
			}
			unchanged, err := os.ReadFile(filepath.Join(frameworkRoot, "go.mod"))
			if err != nil || string(unchanged) != moduleSource {
				t.Fatalf("fixture mutated framework module: %s, err=%v", unchanged, err)
			}
			checksums, err := os.ReadFile(filepath.Join(project, "go.sum"))
			if err != nil || len(checksums) != 0 {
				t.Fatalf("fixture must preserve supplied checksums: %q, err=%v", checksums, err)
			}
		})
	}
}
