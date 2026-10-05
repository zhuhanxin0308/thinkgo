package http

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeIssue53File(t *testing.T, base, name string) {
	t.Helper()
	path := filepath.Join(base, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("static-visibility-marker"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestIssue53StaticHiddenPathsNotServed(t *testing.T) {
	base := t.TempDir()
	for _, name := range []string{
		"public/.env", "public/.git/config", "public/assets/.private/data",
		"public/.well-known/.private", "public/nested/.well-known/secret",
		"public/.well-known/security.txt", "public/assets/site.css",
	} {
		writeIssue53File(t, base, name)
	}
	handler := newTestHTTPHandler(t, newTestHTTPApp(t, base, map[string]interface{}{"enable": false}))
	for _, tc := range []struct {
		path    string
		allowed bool
	}{
		{"/.env", false}, {"/%2eenv", false}, {"/.git/config", false},
		{"/assets/.private/data", false}, {"/.well-known/.private", false},
		{"/nested/.well-known/secret", false}, {"/.well-known/security.txt", true},
		{"/assets/site.css", true}, {"/.well-known%2fsecurity.txt", false},
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			t.Run(method+tc.path, func(t *testing.T) {
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, httptest.NewRequest(method, "http://example.com"+tc.path, nil))
				want := http.StatusNotFound
				if tc.allowed {
					want = http.StatusOK
				}
				if recorder.Code != want {
					t.Fatalf("status=%d want=%d", recorder.Code, want)
				}
				if !tc.allowed && strings.Contains(recorder.Body.String(), "static-visibility-marker") {
					t.Fatal("hidden file bytes exposed")
				}
				if tc.allowed && method == http.MethodGet && recorder.Body.String() != "static-visibility-marker" {
					t.Fatal("public file behavior changed")
				}
			})
		}
	}
}

func TestIssue53StaticSymlinkVisibility(t *testing.T) {
	base := t.TempDir()
	for _, name := range []string{"public/.env", "public/.private/data", "public/visible.txt", "outside.txt"} {
		writeIssue53File(t, base, name)
	}
	for _, tc := range []struct {
		name, target, url string
		allowed           bool
	}{
		{"file-alias", "public/.env", "/file-alias", false},
		{"directory-alias", "public/.private", "/directory-alias/data", false},
		{"outside-alias", "outside.txt", "/outside-alias", false},
		{"visible-alias", "public/visible.txt", "/visible-alias", true},
		{".well-known/alias", "public/.env", "/.well-known/alias", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			link := filepath.Join(base, "public", filepath.FromSlash(tc.name))
			if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(base, filepath.FromSlash(tc.target)), link); err != nil {
				t.Skipf("symlinks unavailable on this host: %v", err)
			}
			handler := newTestHTTPHandler(t, newTestHTTPApp(t, base, map[string]interface{}{"enable": false}))
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://example.com"+tc.url, nil))
			want := http.StatusNotFound
			if tc.allowed {
				want = http.StatusOK
			}
			if recorder.Code != want {
				t.Fatalf("resolved visibility status=%d want=%d", recorder.Code, want)
			}
			if !tc.allowed && strings.Contains(recorder.Body.String(), "static-visibility-marker") {
				t.Fatal("alias exposed hidden or outside file")
			}
			if tc.allowed && recorder.Body.String() != "static-visibility-marker" {
				t.Fatal("visible alias did not serve the actual file")
			}
		})
	}
}
