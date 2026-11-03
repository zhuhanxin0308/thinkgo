package apppath

import (
	"path/filepath"
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
