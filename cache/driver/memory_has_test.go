package driver

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestMemoryHasAvoidsPayloadCopies keeps existence checks independent of payload size.
func TestMemoryHasAvoidsPayloadCopies(t *testing.T) {
	driver := NewMemory()
	if err := driver.Set("large", make([]byte, 64<<10), time.Minute); err != nil {
		t.Fatal(err)
	}
	allocations := testing.AllocsPerRun(100, func() {
		found, err := driver.Has("large")
		if err != nil || !found {
			panic("existing key disappeared")
		}
	})
	if allocations != 0 {
		t.Fatalf("Has copied the payload: %v allocations, want 0", allocations)
	}
}

func TestMemoryHasPreservesLookupBoundaries(t *testing.T) {
	var unavailable *Memory
	if _, err := unavailable.Has("key"); err == nil {
		t.Fatal("nil receiver must fail")
	}
	var empty Memory
	if found, err := empty.Has("missing"); err != nil || found {
		t.Fatalf("zero-value lookup: %v %v", found, err)
	}
	driver, err := NewMemoryWithMaxEntries(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.Set("null", nil, 0); err != nil {
		t.Fatal(err)
	}
	if found, err := driver.Has("null"); err != nil || !found {
		t.Fatalf("nil value is a hit: %v %v", found, err)
	}
	// Move expiry explicitly instead of making a timing-sensitive sleep assertion.
	driver.lock.Lock()
	stored := driver.items["null"]
	stored.expiry = time.Now().Add(-time.Second)
	driver.items["null"] = stored
	driver.lock.Unlock()
	if found, err := driver.Has("null"); err != nil || found {
		t.Fatalf("expired lookup: %v %v", found, err)
	}
	driver.lock.RLock()
	remaining, ordered := len(driver.items), driver.order.Len()
	driver.lock.RUnlock()
	if remaining != 0 || ordered != 0 {
		t.Fatalf("expired item retained: items=%d order=%d", remaining, ordered)
	}
	input := []byte("safe")
	if err := driver.Set("bytes", input, 0); err != nil {
		t.Fatal(err)
	}
	input[0] = 'X'
	value, found, err := driver.Get("bytes")
	if err != nil || !found || string(value.([]byte)) != "safe" {
		t.Fatalf("Set lost ownership isolation: %v %v", value, err)
	}
	value.([]byte)[0] = 'Y'
	again, _, _ := driver.Get("bytes")
	if string(again.([]byte)) != "safe" {
		t.Fatal("Get exposed borrowed cache data")
	}
}

func TestMemoryHasConcurrentMutation(t *testing.T) {
	driver := NewMemory()
	var wait sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for i := 0; i < 100; i++ {
				if err := driver.Set("key", []byte("value"), time.Nanosecond); err != nil {
					t.Error(err)
					return
				}
				if _, err := driver.Has("key"); err != nil {
					t.Error(err)
					return
				}
				if err := driver.Delete("key"); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wait.Wait()
}

func BenchmarkMemoryExistence(b *testing.B) {
	for _, size := range []int{0, 1024, 64 << 10} {
		b.Run(fmt.Sprintf("payload_%d", size), func(b *testing.B) {
			driver := NewMemory()
			if err := driver.Set("key", make([]byte, size), 0); err != nil {
				b.Fatal(err)
			}
			for _, method := range []string{"Has", "Get"} {
				b.Run(method, func(b *testing.B) {
					b.ReportAllocs()
					for i := 0; i < b.N; i++ {
						if method == "Has" {
							if found, err := driver.Has("key"); err != nil || !found {
								b.Fatal(err)
							}
						} else {
							if _, found, err := driver.Get("key"); err != nil || !found {
								b.Fatal(err)
							}
						}
					}
				})
			}
		})
	}
}
