package filesystem

import (
	"errors"
	"mime/multipart"
	"os"
	"path/filepath"
	"testing"
)

func TestUploadRequiresExplicitTrustBeforeOpeningSource(t *testing.T) {
	disk, root := policyTestDisk(t)
	if err := disk.Write("saved.txt", "original"); err != nil {
		t.Fatal(err)
	}
	// This header cannot be opened. The missing policy must reject it first.
	file := &multipart.FileHeader{Filename: "shell.php"}
	called := false
	rule := HashNameRule(func(*multipart.FileHeader) (string, error) {
		called = true
		return "saved", nil
	})
	for _, options := range []map[string]interface{}{nil, {}, {"trusted_upload": false}} {
		if key, err := disk.PutFile("new", file, rule, options); key != "" || !errors.Is(err, ErrUploadRejected) || called {
			t.Fatalf("missing policy reached naming/source: key=%q err=%v called=%t", key, err, called)
		}
		if key, err := disk.PutFileAs("", file, "saved.txt", options); key != "" || !errors.Is(err, ErrUploadRejected) {
			t.Fatalf("missing policy was not rejected: key=%q err=%v", key, err)
		}
	}
	if content, err := disk.Read("saved.txt"); err != nil || content != "original" {
		t.Fatalf("rejection changed the target: %q %v", content, err)
	}
	if _, err := os.Stat(filepath.Join(root, "new")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejection created destination: %v", err)
	}
	for _, options := range []map[string]interface{}{
		{"trusted_upload": "true"}, {"trusted_upload": nil},
		{"trusted_upload": true, "upload_policy": nil},
	} {
		if _, err := disk.PutFileAs("", file, "saved.txt", options); !errors.Is(err, ErrInvalidConfiguration) {
			t.Fatalf("ambiguous trust option accepted: %v", err)
		}
	}
}
