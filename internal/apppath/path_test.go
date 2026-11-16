package apppath

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolve(t *testing.T) {
	base := t.TempDir()
	absolute := filepath.Join(t.TempDir(), "server.pem")
	for _, test := range []struct {
		name       string
		configured string
		want       string
	}{
		{name: "relative", configured: "./runtime/server.pem", want: filepath.Join(base, "runtime", "server.pem")},
		{name: "absolute", configured: absolute, want: absolute},
		{name: "trimmed", configured: " runtime/server.pem ", want: filepath.Join(base, "runtime", "server.pem")},
		{name: "empty", want: base},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := Resolve(base, test.configured); got != test.want {
				t.Fatalf("resolved path = %q, want %q", got, test.want)
			}
		})
	}
}

func TestResolveExistingRetainsMissingSuffix(t *testing.T) {
	base := t.TempDir()
	actualBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, "new", "sessions")
	if got, err := ResolveExisting(path); err != nil || got != filepath.Join(actualBase, "new", "sessions") {
		t.Fatalf("尚不存在的路径尾部应保持不变: %q, %v", got, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("解析路径不应创建目录: %v", err)
	}
}

func TestResolveExistingResolvesLinksAndRejectsDanglingOnes(t *testing.T) {
	base := t.TempDir()
	target := t.TempDir()
	actualTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("Windows 未启用目录链接权限: %v", err)
		}
		t.Fatal(err)
	}
	if got, err := ResolveExisting(filepath.Join(link, "new")); err != nil || got != filepath.Join(actualTarget, "new") {
		t.Fatalf("应解析已存在的目录链接: %q, %v", got, err)
	}
	dangling := filepath.Join(base, "dangling")
	if err := os.Symlink(filepath.Join(base, "missing"), dangling); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveExisting(dangling); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("断开的链接应返回不存在: %v", err)
	}
}
