package command

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestClearRuntimeKeepsOnlyReservedLogCoordinationFiles(t *testing.T) {
	base := t.TempDir()
	logs := filepath.Join(base, "runtime", "log")
	if err := os.MkdirAll(logs, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]bool{
		".thinkgo-log.guard":                        true,
		".thinkgo-log.owner-2026-11-01_2.log.guard": true,
		".thinkgo-log.owner-personal.log.guard":     false,
		"2026-11-01_2.log":                          false,
	}
	for name := range files {
		if err := os.WriteFile(filepath.Join(logs, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := clearRuntimeFiles(base, logs, true); err != nil {
		t.Fatal(err)
	}
	for name, keep := range files {
		_, err := os.Lstat(filepath.Join(logs, name))
		if keep && err != nil || !keep && !errors.Is(err, os.ErrNotExist) {
			t.Errorf("显式清理后的文件状态错误: name=%s keep=%t err=%v", name, keep, err)
		}
	}
}
