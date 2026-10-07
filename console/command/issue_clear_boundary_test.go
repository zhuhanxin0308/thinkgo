package command

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/console"
)

func issue50Clear(t *testing.T, base string, args ...string) error {
	t.Helper()
	command := &Clear{Command: console.Command{App: buildConsoleTestApp(t, base)}}
	return command.Execute(console.NewInput(args...), console.NewOutputWithWriters(&bytes.Buffer{}, &bytes.Buffer{}, false))
}
func issue50AssertFile(t *testing.T, name, want string) {
	t.Helper()
	got, err := os.ReadFile(name)
	if err != nil || string(got) != want {
		t.Fatalf("protected file %q: %q %v", name, got, err)
	}
}

func TestIssue50OutsideTargetsPreserveData(t *testing.T) {
	parent := t.TempDir()
	base := filepath.Join(parent, "project")
	outside := filepath.Join(parent, "project-other")
	for _, dir := range []string{base, outside} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(outside, "keep")
	if err := os.WriteFile(marker, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{outside, filepath.Join("..", "project-other"), base, filepath.Join(base, ".."), filepath.VolumeName(base) + string(filepath.Separator)} {
		if err := issue50Clear(t, base, "--path", target, "--dir"); err == nil {
			t.Errorf("unsafe target accepted: %q", target)
		}
		issue50AssertFile(t, marker, "private")
	}
}

func TestIssue50InProjectCleanupKeepsBoundaries(t *testing.T) {
	for _, absolute := range []bool{false, true} {
		for _, removeDirs := range []bool{false, true} {
			base := t.TempDir()
			for _, rel := range []string{"custom/nested/remove", "custom/.gitignore", "other/keep"} {
				writeClearTestFile(t, base, rel, rel)
			}
			target := "custom"
			if absolute {
				target = filepath.Join(base, target)
			}
			args := []string{"--path", target}
			if removeDirs {
				args = append(args, "--dir")
			}
			if err := issue50Clear(t, base, args...); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(base, "custom", "nested", "remove")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("target was not cleared", err)
			}
			_, err := os.Stat(filepath.Join(base, "custom", "nested"))
			if removeDirs != errors.Is(err, os.ErrNotExist) {
				t.Fatalf("directory flag lost: %v", err)
			}
			issue50AssertFile(t, filepath.Join(base, "custom", ".gitignore"), "custom/.gitignore")
			issue50AssertFile(t, filepath.Join(base, "other", "keep"), "other/keep")
			if err := issue50Clear(t, base, "--path", "not-created"); err != nil {
				t.Fatal("missing child is not a no-op", err)
			}
		}
	}
}

func TestIssue50SymlinkEscapeRejected(t *testing.T) {
	base, outside := t.TempDir(), t.TempDir()
	writeClearTestFile(t, outside, "nested/keep", "private")
	link := filepath.Join(base, "external")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	for _, target := range []string{link, filepath.Join(link, "nested")} {
		if err := issue50Clear(t, base, "--path", target, "--dir"); err == nil {
			t.Error("escaping target accepted")
		}
		issue50AssertFile(t, filepath.Join(outside, "nested", "keep"), "private")
	}
	// Nested links are removed as links, never traversed into their destinations.
	writeClearTestFile(t, base, "custom/remove", "temporary")
	if err := os.Symlink(outside, filepath.Join(base, "custom", "outside-link")); err != nil {
		t.Fatal(err)
	}
	if err := issue50Clear(t, base, "--path", "custom", "--dir"); err != nil {
		t.Fatal(err)
	}
	issue50AssertFile(t, filepath.Join(outside, "nested", "keep"), "private")
}

func TestIssue50ResolvedTargetSwapCannotEscapeProject(t *testing.T) {
	base, outside := t.TempDir(), t.TempDir()
	inside := filepath.Join(base, "custom", "nested")
	if err := os.MkdirAll(inside, 0700); err != nil {
		t.Fatal(err)
	}
	writeClearTestFile(t, outside, "nested/keep", "private")
	app := buildConsoleTestApp(t, base)
	target, _, err := clearTargetPath(app, console.NewInput("--path", "custom/nested"))
	if err != nil {
		t.Fatal(err)
	}
	// Deterministically replace an ancestor after target selection, before opening.
	if err := os.Rename(filepath.Join(base, "custom"), filepath.Join(base, "saved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "custom")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if err := clearRuntimeFiles(base, target, true); err == nil {
		t.Fatal("resolved target reopened outside project")
	}
	issue50AssertFile(t, filepath.Join(outside, "nested", "keep"), "private")
}
