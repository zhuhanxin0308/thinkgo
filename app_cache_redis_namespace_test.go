package framework

import (
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/zhuhanxin0308/thinkgo/v3/cache"
	redisDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver/redis"
)

func TestAppRedisNamespacePrefixCannotEscapeFraming(t *testing.T) {
	server := miniredis.RunT(t)
	host, portText, err := net.SplitHostPort(server.Addr())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	const emptyNamespace = "__thinkgo_namespace__:v2:0::"
	const firstPrefix = "tenant:"
	const secondPrefix = firstPrefix + emptyNamespace + "vip:"
	const collisionKey = "vip:" + emptyNamespace + "client"
	storeConfig := func(prefix string) map[string]interface{} {
		return map[string]interface{}{"type": "redis", "host": host, "port": port, "prefix": prefix}
	}
	configuration := map[string]interface{}{
		"default": "first",
		"stores": map[string]interface{}{
			"first":  storeConfig(firstPrefix),
			"second": storeConfig(secondPrefix),
		},
	}
	manager, err := createAppCache(&App{BasePath: t.TempDir()}, configuration)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	second, err := manager.Store("second")
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Set("client", "protected", 0); err != nil {
		t.Fatal(err)
	}
	if value, found, err := manager.Get(collisionKey); err != nil || found {
		t.Fatalf("first Redis namespace disclosed second's entry: value=%v found=%t err=%v", value, found, err)
	}
	assertProtected := func(t *testing.T) {
		t.Helper()
		if value, found, err := second.Get("client"); err != nil || !found || value != "protected" {
			t.Fatalf("first Redis namespace changed second's entry: value=%v found=%t err=%v", value, found, err)
		}
	}
	for name, mutate := range map[string]func() error{
		"overwrite": func() error { return manager.Set(collisionKey, "local", 0) },
		"delete":    func() error { return manager.Forget(collisionKey) },
		"prefix":    func() error { return manager.ClearPrefix("vip:") },
		"flush":     manager.Flush,
	} {
		t.Run(name, func(t *testing.T) {
			if err := manager.Set(collisionKey, "local", 0); err != nil {
				t.Fatal(err)
			}
			assertProtected(t)
			if err := mutate(); err != nil {
				t.Fatal(err)
			}
			assertProtected(t)
			if name != "overwrite" {
				if _, found, err := manager.Get(collisionKey); err != nil || found {
					t.Fatalf("%s did not remove its own entry: found=%t err=%v", name, found, err)
				}
			}
		})
	}

	// Configuration and runtime identities must still reject the same namespace,
	// even though all application Redis clients now use an empty raw prefix.
	duplicate := map[string]interface{}{
		"default": "first",
		"stores": map[string]interface{}{
			"first":  storeConfig(firstPrefix),
			"second": storeConfig(firstPrefix),
		},
	}
	if duplicateManager, err := createAppCache(&App{BasePath: t.TempDir()}, duplicate); err == nil || duplicateManager != nil {
		t.Fatalf("duplicate configured Redis namespace accepted: manager=%v err=%v", duplicateManager, err)
	}
	spec, err := parseAppCacheStore(func(path string) (string, error) { return path, nil }, "copy", storeConfig(firstPrefix))
	if err != nil {
		t.Fatal(err)
	}
	copyDriver, err := createAppCacheDriver(spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeCacheDriver(copyDriver) })
	if err := manager.RegisterStore("copy", copyDriver); !errors.Is(err, cache.ErrCacheStoreExists) {
		t.Fatalf("duplicate runtime Redis namespace accepted: %v", err)
	}
	if prefix := spec.redisConfig["prefix"]; prefix != firstPrefix {
		t.Fatalf("driver construction mutated configured Redis identity: %v", prefix)
	}
}

func TestAppRedisNamespaceValidatesPrefixBeforeConstruction(t *testing.T) {
	for _, test := range []struct {
		prefix string
		valid  bool
	}{
		{prefix: strings.Repeat("x", 256), valid: true},
		{prefix: strings.Repeat("x", 257)},
		{prefix: strings.Repeat("é", 128), valid: true},
		{prefix: strings.Repeat("é", 129)},
		{prefix: "tenant:__thinkgo_namespace__:v2:0::vip:", valid: true},
		{prefix: "tenant:__thinkgo_lock__:"},
		{prefix: "tenant:__thinkgo_cache_fence__:"},
	} {
		spec, err := parseAppCacheStore(func(path string) (string, error) { return path, nil }, "redis", map[string]interface{}{
			"type": "redis", "prefix": test.prefix,
		})
		if test.valid {
			if err != nil {
				t.Fatalf("valid %d-byte prefix rejected during parsing: %v", len(test.prefix), err)
			}
		} else {
			if !errors.Is(err, redisDriver.ErrInvalidRedisConfig) {
				t.Fatalf("invalid prefix accepted during parsing: %v", err)
			}
			// Direct internal construction must enforce the same restriction.
			spec = appCacheStoreSpec{
				driverType:  "redis",
				prefix:      test.prefix,
				redisConfig: map[string]interface{}{"prefix": test.prefix},
			}
		}
		driver, err := createAppCacheDriver(spec)
		if test.valid {
			if err != nil {
				t.Fatalf("valid %d-byte prefix rejected: %v", len(test.prefix), err)
			}
			if err := closeCacheDriver(driver); err != nil {
				t.Fatal(err)
			}
		} else if driver != nil || !errors.Is(err, redisDriver.ErrInvalidRedisConfig) {
			t.Fatalf("invalid prefix accepted during construction: driver=%v err=%v", driver, err)
		}
	}
}
