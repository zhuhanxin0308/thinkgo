package command

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func requireDistributionMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != want {
		t.Fatalf("%s permissions=%04o, want %04o", path, info.Mode().Perm(), want)
	}
}

// Observe ordinary creation through the real filesystem so public-resource
// expectations preserve the caller's umask while private configs stay 0600.
func distributionCreationModes(t *testing.T) (os.FileMode, os.FileMode) {
	t.Helper()
	base := t.TempDir()
	file := filepath.Join(base, "public-file")
	directory := filepath.Join(base, "public-directory")
	if err := os.WriteFile(file, nil, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(directory, 0755); err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	directoryInfo, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	return fileInfo.Mode().Perm(), directoryInfo.Mode().Perm()
}

func TestDistributionConfigurationPermissions(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "new"
		if existing {
			name = "replace"
		}
		t.Run(name, func(t *testing.T) {
			publicMode, directoryMode := distributionCreationModes(t)
			if existing {
				publicMode = 0644
			}
			sourcePath, targetPath := t.TempDir(), t.TempDir()
			private := []string{"config/database.json", "config/nested/secret.JSON", "app/admin/config/app.json", "app/admin/config/nested/database.json"}
			public := []string{"public/config/database.json", "public/style.css", "app/admin/lang/en.json", "app/lang/en.json", "app/lang/config/en.json", "app/view/config/example.json", "view/index.html", "app/admin/view/config/example.json"}
			all := append(append([]string{}, private...), public...)
			for _, path := range all {
				writeArtifactFixture(t, sourcePath, path, "copied-content")
				if err := os.Chmod(filepath.Join(sourcePath, path), 0600); err != nil {
					t.Fatal(err)
				}
				if existing {
					writeArtifactFixture(t, targetPath, path, "old-content-must-be-truncated")
					if err := os.Chmod(filepath.Join(targetPath, path), 0644); err != nil {
						t.Fatal(err)
					}
				}
			}
			source, err := os.OpenRoot(sourcePath)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			target, err := os.OpenRoot(targetPath)
			if err != nil {
				t.Fatal(err)
			}
			defer target.Close()
			for _, directory := range []string{"config", "app", "public", "view"} {
				if err := copyDistributionTree(context.Background(), source, target, directory, directory == "config"); err != nil {
					t.Fatal(err)
				}
			}
			for _, path := range all {
				content, err := os.ReadFile(filepath.Join(targetPath, path))
				if err != nil || string(content) != "copied-content" {
					t.Fatalf("%s content=%q error=%v", path, content, err)
				}
			}
			for _, path := range private {
				requireDistributionMode(t, filepath.Join(targetPath, path), 0600)
			}
			for _, path := range public {
				requireDistributionMode(t, filepath.Join(targetPath, path), publicMode)
			}
			for _, path := range []string{"config", "config/nested", "app/admin/config", "public", "view"} {
				requireDistributionMode(t, filepath.Join(targetPath, path), directoryMode)
			}
		})
	}
}

func TestBuildDistributionConfigurationPermissions(t *testing.T) {
	publicMode, directoryMode := distributionCreationModes(t)
	base := t.TempDir()
	for path, content := range map[string]string{
		"config/app.json": `{}`, "config/database.json": `{"password":"test-only-secret"}`,
		"app/admin/config/app.json": `{}`, "app/admin/config/database.json": `{"key":"test-only-key"}`,
		// A configured resource can include app/admin again after the config walk.
		"app/admin/config/view.json": `{"view_path":"app/admin"}`,
		"app/admin/lang/en.json":     `{}`, "public/site.css": "css", "app/admin/view/index.html": "template",
	} {
		writeArtifactFixture(t, base, path, content)
		if err := os.Chmod(filepath.Join(base, path), 0600); err != nil {
			t.Fatal(err)
		}
	}
	target, err := parseBuildTarget("linux/amd64", "")
	if err != nil {
		t.Fatal(err)
	}
	compile := func(_ context.Context, _, executable string, _ buildTarget) error {
		return os.WriteFile(executable, []byte("binary"), 0755)
	}
	for i := 0; i < 2; i++ {
		if err := buildDistribution(context.Background(), base, target, compile); err != nil {
			t.Fatal(err)
		}
		distribution := filepath.Join(base, "dist", target.directory())
		for _, path := range []string{"config/app.json", "config/database.json", "app/admin/config/app.json", "app/admin/config/database.json", "app/admin/config/view.json"} {
			requireDistributionMode(t, filepath.Join(distribution, path), 0600)
			if i == 0 {
				if err := os.Chmod(filepath.Join(distribution, path), 0644); err != nil {
					t.Fatal(err)
				}
			}
		}
		for _, path := range []string{"app/admin/lang/en.json", "public/site.css", "app/admin/view/index.html", "Dockerfile", "docker-compose.yml"} {
			requireDistributionMode(t, filepath.Join(distribution, path), publicMode)
		}
		requireDistributionMode(t, filepath.Join(distribution, "config"), directoryMode)
		dockerfile, err := os.ReadFile(filepath.Join(distribution, "Dockerfile"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(dockerfile), "COPY --chown=65532:65532 . /app/") || !strings.Contains(string(dockerfile), "USER 65532:65532") {
			t.Fatal("private image configuration must belong to the non-root runtime user")
		}
		compose, err := os.ReadFile(filepath.Join(distribution, "docker-compose.yml"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(compose), "./config") || !strings.Contains(string(compose), "      - runtime:/app/runtime") {
			t.Fatal("default compose must use image configuration and retain the runtime volume")
		}
	}
}
