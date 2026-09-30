package filesystem

import (
	"net/url"
	"testing"
)

// URL accepts raw logical names, not pre-escaped URL paths. No actual file is
// needed, so reserved filename tests are also valid on Windows.
func TestAuditLocalURLMustPreserveFilenameMeaning(t *testing.T) {
	for _, prefix := range []string{"/storage", "https://cdn.example.test/storage/"} {
		disk, err := NewLocal(LocalConfig{Root: t.TempDir(), URL: prefix})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := disk.Close(); err != nil {
				t.Error(err)
			}
		})
		for _, name := range []string{"", "reports/plain.txt", "reports/q1#draft.txt", "reports/q1?draft.txt", "reports/100%zz.txt", "reports/%2F.txt", "报告/一 月.txt"} {
			generated, err := disk.URL(name)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := url.Parse(generated)
			if err != nil {
				t.Fatalf("invalid generated URL %q: %v", generated, err)
			}
			if parsed.Path != "/storage/"+name || parsed.RawQuery != "" || parsed.Fragment != "" {
				t.Fatalf("filename changed meaning: name=%q URL=%q", name, generated)
			}
		}
	}
}

func TestLocalFileURLPreservesConfiguredURLComponents(t *testing.T) {
	generated, err := localFileURL("https://cdn.example.test/base%2F?download=1#anchor", "q?#%.txt")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(generated)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != "cdn.example.test" || parsed.Path != "/base//q?#%.txt" ||
		parsed.EscapedPath() != "/base%2F/q%3F%23%25.txt" || parsed.RawQuery != "download=1" || parsed.Fragment != "anchor" {
		t.Fatalf("prefix components or filename lost: %#v", parsed)
	}
	for _, invalid := range []string{"/bad%zz", "https:opaque"} {
		if _, err := localFileURL(invalid, "file.txt"); err == nil {
			t.Fatalf("invalid prefix accepted: %q", invalid)
		}
	}
}
