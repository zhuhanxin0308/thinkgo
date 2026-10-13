package filesystem

import (
	"errors"
	"testing"
)

func TestUploadPolicyASCIIValidationPrecedesCaseNormalization(t *testing.T) {
	// U+212A folds to ASCII k. Reject it before folding, including trusted config.
	if policy, err := NewUploadPolicy(UploadPolicyConfig{MaxBytes: 10, AllowedTypes: map[string][]string{"K": {"text/plain"}}}); policy != nil || !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("non-ASCII configured extension accepted: %v", err)
	}
	disk, _ := policyTestDisk(t)
	options := policyTestOptions(t, 10, map[string][]string{".K": {"text/plain"}})
	for _, test := range []struct{ source, target string }{
		{"x.k", "bad.K"},
		{"x.K", "bad.k"},
	} {
		file := policyTestFile(t, test.source, []byte("hello"), false)
		key, err := disk.PutFileAs("", file, test.target, options)
		if key != "" || !errors.Is(err, ErrUploadTypeNotAllowed) {
			t.Errorf("non-ASCII source/final extension accepted: %+v key=%q err=%v", test, key, err)
		}
	}
	file := policyTestFile(t, "x.K", []byte("hello"), false)
	if key, err := disk.PutFile("", file, "sha256", options); key != "" || !errors.Is(err, ErrUploadTypeNotAllowed) {
		t.Errorf("generated name accepted non-ASCII source extension: %q %v", key, err)
	}
	file = policyTestFile(t, "x.K", []byte("hello"), false)
	if key, err := disk.PutFileAs("", file, "UPPER.K", options); err != nil || key != "UPPER.K" {
		t.Fatalf("valid ASCII case-insensitive extension changed: %q %v", key, err)
	}
}
