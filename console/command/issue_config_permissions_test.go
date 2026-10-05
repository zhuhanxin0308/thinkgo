package command

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/console"
)

func TestIssue31ConfigCacheRestrictsNewAndExistingFiles(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing_%v", existing), func(t *testing.T) {
			base := t.TempDir()
			ensureConsoleTestConfigFiles(t, base)
			if err := os.WriteFile(filepath.Join(base, "config", "credentials.json"), []byte(`{"api_key":"test-only-credential"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			app := buildConsoleTestApp(t, base)
			target := filepath.Join(base, "runtime", "config.json")
			if existing {
				if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(target, []byte(`{}`), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(target, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			cmd := &OptimizeConfig{Command: console.Command{App: app}}
			if err := cmd.Execute(console.NewInput(), console.NewOutputWithWriters(&bytes.Buffer{}, &bytes.Buffer{}, false)); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			// Windows requires ACL verification; still exercise creation/replacement and content there.
			if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
				t.Errorf("configuration exposed by mode %o", info.Mode().Perm())
			}
			data, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			if !json.Valid(data) || !bytes.Contains(data, []byte("test-only-credential")) {
				t.Error("configuration cache lost its reloadable content")
			}
			leftovers, err := filepath.Glob(target + ".tmp-*")
			if err != nil || len(leftovers) != 0 {
				t.Errorf("temporary files remain: %v, %v", leftovers, err)
			}
		})
	}
}

func TestIssue31NonConfigurationWritesPreservePermissions(t *testing.T) {
	base := t.TempDir()
	app := buildConsoleTestApp(t, base)
	target := filepath.Join(base, "published.txt")
	if err := os.WriteFile(target, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := writeProjectFileAtomically(app, target, []byte("new")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
		t.Errorf("non-configuration mode changed: %o", info.Mode().Perm())
	}
}
