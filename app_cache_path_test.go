package framework

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/cache"
	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
)

func TestAppCachePhysicalDirectoryIsIndependentOfNamespace(t *testing.T) {
	basePath := t.TempDir()
	runtimePath := filepath.Join(basePath, "runtime")
	cachePath := filepath.Join(runtimePath, "cache")
	backend, err := cacheDriver.NewFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	expectedPath, err := filepath.EvalSymlinks(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	identities := make(map[string]bool)
	for _, prefix := range []string{"", "tenant:|", "tenant:|nested:"} {
		wrapped, err := cache.NewNamespaceDriver(backend, prefix, "")
		if err != nil {
			t.Fatal(err)
		}
		if path := wrapped.CacheDirectory(); path != expectedPath {
			t.Fatalf("namespace %q changed the physical directory: %q", prefix, path)
		}
		identity := wrapped.CacheResourceIdentity()
		if identity == "" || identities[identity] {
			t.Fatalf("namespace %q did not retain an independent identity: %q", prefix, identity)
		}
		identities[identity] = true
		if err := validateAppCacheDriverPathWithinRuntime(runtimePath, wrapped); err != nil {
			t.Fatalf("valid physical directory rejected for namespace %q: %v", prefix, err)
		}
		if err := validateAppCacheDriverPathWithinRuntime(filepath.Join(basePath, "other-runtime"), wrapped); err == nil {
			t.Fatalf("namespace %q bypassed physical directory confinement", prefix)
		}
	}

	var missingFile *cacheDriver.File
	var missingNamespace *cache.NamespaceDriver
	if missingFile.CacheDirectory() != "" || missingNamespace.CacheDirectory() != "" {
		t.Fatal("missing drivers must not report a directory")
	}
	memory := cacheDriver.NewMemory()
	wrapped, err := cache.NewNamespaceDriver(memory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if wrapped.CacheDirectory() != "" {
		t.Fatal("memory cache must not report a directory")
	}
	for _, driver := range []cache.Driver{memory, wrapped} {
		if err := validateAppCacheDriverPathWithinRuntime(runtimePath, driver); err == nil {
			t.Fatal("file directory validation accepted a driver without a directory")
		}
	}
}

func TestCreateAppCacheFileNamespaceStartup(t *testing.T) {
	basePath := t.TempDir()
	prefixes := []string{"", "tenant:|", "tenant:|nested:"}
	managers := make([]*cache.Cache, 0, len(prefixes))
	for _, prefix := range prefixes {
		manager, err := createAppCache(&App{BasePath: basePath}, map[string]interface{}{
			"default": "file",
			"stores": map[string]interface{}{
				"file": map[string]interface{}{"type": "file", "path": "runtime/cache", "prefix": prefix},
			},
		})
		if err != nil {
			t.Fatalf("file cache startup failed for namespace %q: %v", prefix, err)
		}
		t.Cleanup(func() { _ = manager.Close() })
		managers = append(managers, manager)
		if err := manager.Set("shared-key", prefix, 0); err != nil {
			t.Fatal(err)
		}
	}
	for index, manager := range managers {
		value, found, err := manager.Get("shared-key")
		if err != nil || !found || value != prefixes[index] {
			t.Fatalf("file namespace %q lost isolation: value=%v found=%t err=%v", prefixes[index], value, found, err)
		}
	}
	if err := managers[0].Flush(); err != nil {
		t.Fatal(err)
	}
	for index, manager := range managers[1:] {
		value, found, err := manager.Get("shared-key")
		if err != nil || !found || value != prefixes[index+1] {
			t.Fatalf("empty namespace flush affected another namespace: value=%v found=%t err=%v", value, found, err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(basePath, "runtime"))
	if err != nil || len(entries) != 1 || entries[0].Name() != "cache" || !entries[0].IsDir() {
		t.Fatalf("file namespace created an unexpected physical directory: entries=%v err=%v", entries, err)
	}
}
