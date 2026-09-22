package cache

import (
	"context"
	"fmt"
	"testing"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
)

func benchmarkScopedReads(b *testing.B, manager *Cache, scopes int) {
	b.Helper()
	backend, release, err := manager.driver()
	if err != nil {
		b.Fatal(err)
	}
	defer release()
	keys := make([]string, scopes)
	for index := range keys {
		keys[index] = fmt.Sprintf("scope:%d", index)
	}
	if _, err := manager.beginKeyInvalidation(context.Background(), backend, keys); err != nil {
		b.Fatal(err)
	}
	if err := manager.Set("scope:read", "value"); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if value, found, err := manager.Get("scope:read"); err != nil || !found || value != "value" {
			b.Fatalf("业务读失败: %v %t %v", value, found, err)
		}
	}
}

// BenchmarkScopedReads 比较无失效记录与一千精确范围下的读成本，确保分桶不将所有范围带入每次读取。
func BenchmarkScopedReads(b *testing.B) {
	for _, scopes := range []int{0, 1000} {
		for _, kind := range []string{"memory", "file"} {
			b.Run(fmt.Sprintf("%s/%d", kind, scopes), func(b *testing.B) {
				var backend Driver = cacheDriver.NewMemory()
				if kind == "file" {
					file, err := cacheDriver.NewFile(b.TempDir())
					if err != nil {
						b.Fatal(err)
					}
					backend = file
				}
				benchmarkScopedReads(b, NewCache(nil, backend), scopes)
			})
		}
	}
}
