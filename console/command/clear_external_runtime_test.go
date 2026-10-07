package command

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/console"
)

// TestIssue50ExternalRuntimeCacheModes 保留可信外部 runtime 的缓存服务与缓存格式语义。
func TestIssue50ExternalRuntimeCacheModes(t *testing.T) {
	for _, test := range []struct {
		name   string
		args   []string
		expire bool
		dirs   bool
	}{
		{name: "default"},
		{name: "cache", args: []string{"--cache"}},
		{name: "directories", args: []string{"--cache", "--dir"}, dirs: true},
		{name: "expired", args: []string{"--cache", "--expire"}, expire: true},
		{name: "expired_directories", args: []string{"--cache", "--expire", "--dir"}, expire: true, dirs: true},
		{name: "cache_precedes_path", args: []string{"--cache", "--path", ".."}},
		{name: "cache_precedes_log", args: []string{"--cache", "--log"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			base, runtime := t.TempDir(), t.TempDir()
			app := buildConsoleTestApp(t, base)
			if err := app.SetRuntimePath(runtime); err != nil {
				t.Fatal(err)
			}
			if err := app.Cache().Set("managed", "service-value"); err != nil {
				t.Fatal(err)
			}
			for key, expiry := range map[string]time.Time{"expired": time.Now().Add(-time.Hour), "fresh": time.Now().Add(time.Hour)} {
				name := fmt.Sprintf("%x.cache", sha256.Sum256([]byte(key)))
				body := fmt.Sprintf(`{"key":%q,"value":"cached","expiry":%q}`, key, expiry.UTC().Format(time.RFC3339Nano))
				writeClearTestFile(t, runtime, "cache/"+name, body)
			}
			for _, name := range []string{"cache/unmanaged", "cache/.gitignore", "log/keep", "other/keep"} {
				writeClearTestFile(t, runtime, name, "protected")
			}
			empty := filepath.Join(runtime, "cache", "empty")
			if err := os.MkdirAll(filepath.Join(empty, "nested"), 0o700); err != nil {
				t.Fatal(err)
			}
			command := &Clear{Command: console.Command{App: app}}
			output := console.NewOutputWithWriters(&bytes.Buffer{}, &bytes.Buffer{}, false)
			if err := command.Execute(console.NewInput(test.args...), output); err != nil {
				t.Fatalf("external runtime cache rejected: %v", err)
			}
			if _, found, err := app.Cache().Get("managed"); err != nil || found != test.expire {
				t.Fatalf("backend Flush contract: found=%v err=%v", found, err)
			}
			for _, key := range []string{"expired", "fresh"} {
				name := filepath.Join(runtime, "cache", fmt.Sprintf("%x.cache", sha256.Sum256([]byte(key))))
				_, err := os.Stat(name)
				kept := test.expire && key == "fresh"
				if (kept && err != nil) || (!kept && !errors.Is(err, os.ErrNotExist)) {
					t.Fatalf("cache key=%s kept=%v err=%v", key, kept, err)
				}
			}
			_, err := os.Stat(empty)
			if (test.dirs && !errors.Is(err, os.ErrNotExist)) || (!test.dirs && err != nil) {
				t.Fatalf("empty directories: remove=%v err=%v", test.dirs, err)
			}
			for _, name := range []string{"cache/unmanaged", "cache/.gitignore", "log/keep", "other/keep"} {
				content, err := os.ReadFile(filepath.Join(runtime, filepath.FromSlash(name)))
				if err != nil || string(content) != "protected" {
					t.Fatalf("non-cache data changed: %s content=%q err=%v", name, content, err)
				}
			}
		})
	}
}

// TestIssue50ExternalRuntimeStillRejectsFileModes 不因允许缓存模式而放开 --path/--log。
func TestIssue50ExternalRuntimeStillRejectsFileModes(t *testing.T) {
	base, runtime := t.TempDir(), t.TempDir()
	app := buildConsoleTestApp(t, base)
	if err := app.SetRuntimePath(runtime); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"cache/keep", "log/keep"} {
		writeClearTestFile(t, runtime, relative, "protected")
	}
	command := &Clear{Command: console.Command{App: app}}
	for _, args := range [][]string{{"--log"}, {"--path", filepath.Join(runtime, "cache"), "--dir"}} {
		if err := command.Execute(console.NewInput(args...), console.NewOutputWithWriters(&bytes.Buffer{}, &bytes.Buffer{}, false)); err == nil {
			t.Fatalf("external file mode accepted: %v", args)
		}
	}
	for _, relative := range []string{"cache/keep", "log/keep"} {
		content, err := os.ReadFile(filepath.Join(runtime, filepath.FromSlash(relative)))
		if err != nil || string(content) != "protected" {
			t.Fatalf("external data changed: %s %q %v", relative, content, err)
		}
	}
}
