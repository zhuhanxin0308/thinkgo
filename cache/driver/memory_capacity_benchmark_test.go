package driver

import (
	"fmt"
	"testing"
)

// Exclude population and key formatting from the timed section. Cycle through
// capacity+2 keys so every measured write inserts a missing key and requires FIFO.
func BenchmarkMemoryCapacityInsertFull(b *testing.B) {
	for _, capacity := range []int{128, 2048, 8192} {
		b.Run(fmt.Sprint(capacity), func(b *testing.B) {
			m, err := NewMemoryWithMaxEntries(capacity)
			if err != nil {
				b.Fatal(err)
			}
			keys := make([]string, capacity+2)
			for i := range keys {
				keys[i] = fmt.Sprintf("entry-%d", i)
			}
			for i := 0; i < capacity; i++ {
				if err := m.Set(keys[i], int64(1), 0); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := m.Set(keys[(i+capacity)%len(keys)], int64(1), 0); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
