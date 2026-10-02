package cache

import (
	"fmt"
	"testing"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
)

// BenchmarkCacheHasMemory includes the facade's fencing checks, not only the driver's fast path.
func BenchmarkCacheHasMemory(b *testing.B) {
	for _, size := range []int{1024, 64 << 10} {
		b.Run(fmt.Sprintf("payload_%d", size), func(b *testing.B) {
			manager := NewCache(nil, cacheDriver.NewMemory())
			defer func() { _ = manager.Close() }()
			if err := manager.Set("key", make([]byte, size), 0); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if found, err := manager.Has("key"); err != nil || !found {
					b.Fatal(err)
				}
			}
		})
	}
}
