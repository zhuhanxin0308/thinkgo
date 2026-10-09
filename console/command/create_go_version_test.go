package command

import (
	"go/version"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/mod/modfile"
)

// TestScaffoldGoVersionMeetsFrameworkRequirement prevents generated projects
// from silently retaining a lower Go requirement after a security update.
func TestScaffoldGoVersionMeetsFrameworkRequirement(t *testing.T) {
	modulePath := filepath.Join("..", "..", "go.mod")
	frameworkSource, err := os.ReadFile(modulePath)
	if err != nil {
		t.Fatal(err)
	}
	frameworkModule, err := modfile.ParseLax(modulePath, frameworkSource, nil)
	if err != nil {
		t.Fatal(err)
	}
	if frameworkModule.Go == nil || !version.IsValid("go"+frameworkModule.Go.Version) {
		t.Fatal("framework module has no valid Go requirement")
	}
	sources, err := projectSources("example.com/go-version-contract")
	if err != nil {
		t.Fatal(err)
	}
	generated, err := modfile.ParseLax("go.mod", sources["go.mod"], nil)
	if err != nil {
		t.Fatal(err)
	}
	if generated.Go == nil || !version.IsValid("go"+generated.Go.Version) {
		t.Fatal("generated project has no valid Go requirement")
	}
	if version.Compare("go"+generated.Go.Version, "go"+frameworkModule.Go.Version) < 0 {
		t.Fatalf("generated Go requirement %s is older than framework requirement %s", generated.Go.Version, frameworkModule.Go.Version)
	}
}
