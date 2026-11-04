package regularfile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenReturnsReadOnlyRegularDescriptor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(path, []byte("report-body"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, info, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	expected, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || !os.SameFile(info, expected) {
		t.Fatalf("returned metadata does not identify the regular file: %#v", info)
	}
	content, err := io.ReadAll(file)
	if err != nil || string(content) != "report-body" {
		t.Fatalf("read: content=%q err=%v", content, err)
	}
	if count, err := file.Write([]byte("unexpected")); err == nil || count != 0 {
		t.Fatalf("descriptor must remain read-only: count=%d err=%v", count, err)
	}
}

func TestOpenRejectsMissingAndNonRegularFiles(t *testing.T) {
	directory := t.TempDir()
	for _, test := range []struct {
		name string
		path string
		want error
	}{
		{name: "missing", path: filepath.Join(directory, "missing"), want: os.ErrNotExist},
		{name: "directory", path: directory, want: ErrNotRegular},
	} {
		t.Run(test.name, func(t *testing.T) {
			file, info, err := Open(test.path)
			if file != nil {
				_ = file.Close()
			}
			if file != nil || info != nil || !errors.Is(err, test.want) {
				t.Fatalf("rejected open must not transfer a handle: file=%v info=%v err=%v", file, info, err)
			}
		})
	}
}

func TestValidationClosesRejectedDescriptor(t *testing.T) {
	file, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	opened, info, err := validateOpened(file)
	if opened != nil || info != nil || !errors.Is(err, ErrNotRegular) {
		t.Fatalf("directory was accepted: opened=%v info=%v err=%v", opened, info, err)
	}
	if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("rejected descriptor was not closed: %v", err)
	}
}

func TestValidationPreservesDescriptorStatError(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "closed-")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	opened, info, err := validateOpened(file)
	if opened != nil || info != nil || !errors.Is(err, os.ErrClosed) {
		t.Fatalf("descriptor error was lost: opened=%v info=%v err=%v", opened, info, err)
	}
}
