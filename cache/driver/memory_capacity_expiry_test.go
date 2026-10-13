package driver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func requireMemoryValue(t *testing.T, memory *Memory, key string, want interface{}) {
	t.Helper()
	got, found, err := memory.Get(key)
	if err != nil || !found || got != want {
		t.Fatalf("Get(%q)=(%v,%t,%v), want %v", key, got, found, err, want)
	}
}

func TestMemoryCapacityReclaimsExpiredProtectedItems(t *testing.T) {
	writers := map[string]func(*Memory, string) error{
		"set": func(m *Memory, key string) error { return m.Set(key, int64(7), 0) },
		"update": func(m *Memory, key string) error {
			return m.Update(key, 0, func(interface{}, bool) (interface{}, bool, error) { return int64(7), false, nil })
		},
		"preserve": func(m *Memory, key string) error {
			return m.UpdatePreserveTTL(key, func(interface{}, bool) (interface{}, bool, error) { return int64(7), false, nil })
		},
		"conditional-ttl": func(m *Memory, key string) error {
			return m.UpdatePreserveTTLConditionally(key, func(interface{}, bool) (interface{}, bool, bool, error) { return int64(7), false, false, nil })
		},
		"increment": func(m *Memory, key string) error { _, err := m.Inc(key, 7); return err },
		"decrement": func(m *Memory, key string) error { _, err := m.Dec(key, 7); return err },
		"lease-owner": func(m *Memory, key string) error {
			ok, err := m.UpdateIfLockOwnerContext(context.Background(), key, 0, "lease", "owner", func(interface{}, bool) (interface{}, bool, error) { return int64(7), false, nil })
			if err == nil && !ok {
				return errors.New("valid lease did not commit")
			}
			return err
		},
	}
	for name, write := range writers {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				m, err := NewMemoryWithMaxEntries(1)
				if err != nil {
					t.Fatal(err)
				}
				if ok, err := m.AcquireLock("lease", "owner", time.Minute); err != nil || !ok {
					t.Fatal("acquire", ok, err)
				}
				if err := m.Set(cacheFenceMetadataPrefix+"scope", "fence", time.Minute); err != nil {
					t.Fatal(err)
				}
				if err := m.Set(cacheTagMetadataPrefix+"expired", "old", time.Second); err != nil {
					t.Fatal(err)
				}
				time.Sleep(time.Second) // At expiry, but strictly before the periodic sweep.
				key := cacheTagReverseMetadataPrefix + "replacement"
				if err := write(m, key); err != nil {
					t.Fatalf("expired item blocked %s: %v", name, err)
				}
				want := int64(7)
				if name == "decrement" {
					want = -7
				}
				requireMemoryValue(t, m, key, want)
				requireMemoryValue(t, m, cacheFenceMetadataPrefix+"scope", "fence")
				if ok, err := m.RenewLock("lease", "owner", time.Minute); err != nil || !ok {
					t.Fatal("lease lost", ok, err)
				}
				if err := m.Set("blocked", "value", 0); !errors.Is(err, ErrMemoryCapacityExhausted) {
					t.Fatalf("live protected capacity must still reject: %v", err)
				}
			})
		})
	}
}

func TestMemoryCapacityExpiresBeforeEvictingLiveFIFO(t *testing.T) {
	for _, expiredKey := range []string{"newer-expired", cacheTagMetadataPrefix + "expired"} {
		t.Run(expiredKey, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				m, err := NewMemoryWithMaxEntries(2)
				if err != nil {
					t.Fatal(err)
				}
				if err := m.Set("oldest-live", "keep", time.Hour); err != nil {
					t.Fatal(err)
				}
				if err := m.Set(expiredKey, "expire", time.Second); err != nil {
					t.Fatal(err)
				}
				time.Sleep(time.Second)
				if err := m.Set("new", "new", 0); err != nil {
					t.Fatal(err)
				}
				requireMemoryValue(t, m, "oldest-live", "keep")
				requireMemoryValue(t, m, "new", "new")
				if err := m.Set("next", "next", 0); err != nil {
					t.Fatal(err)
				}
				if _, found, err := m.Get("oldest-live"); err != nil || found {
					t.Fatalf("live FIFO changed: found=%t err=%v", found, err)
				}
				requireMemoryValue(t, m, "new", "new")
			})
		})
	}
}

func TestMemoryCapacityDeadlineChangesAndCommitTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, err := NewMemoryWithMaxEntries(1)
		if err != nil {
			t.Fatal(err)
		}
		key := cacheTagMetadataPrefix + "renewed"
		if err := m.Set(key, "old", time.Second); err != nil {
			t.Fatal(err)
		}
		if err := m.Set(key, "renewed", 10*time.Second); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
		if err := m.Set("no-space", 1, 0); !errors.Is(err, ErrMemoryCapacityExhausted) {
			t.Fatalf("renewed value was reclaimed: %v", err)
		}
		requireMemoryValue(t, m, key, "renewed")
		// Capacity must use the time after this callback, not updateAtomic's initial time.
		if err := m.Update(cacheTagMetadataPrefix+"after-callback", time.Second, func(interface{}, bool) (interface{}, bool, error) {
			time.Sleep(9 * time.Second)
			return "committed", false, nil
		}); err != nil {
			t.Fatalf("expired during callback blocked commit: %v", err)
		}
		requireMemoryValue(t, m, cacheTagMetadataPrefix+"after-callback", "committed")
		time.Sleep(time.Second)
		if err := m.Set("final", "final", 0); err != nil {
			t.Fatal(err)
		}
		requireMemoryValue(t, m, "final", "final")
	})
}

// This independent oracle counts physical non-exempt records, including records
// whose expiration has not yet been observed. It does not use the cached count.
func assertMemoryCapacityAccounting(t *testing.T, m *Memory) {
	t.Helper()
	m.lock.RLock()
	defer m.lock.RUnlock()
	want := 0
	for key := range m.items {
		if !strings.HasPrefix(key, cacheFenceMetadataPrefix) {
			want++
		}
	}
	if got := m.capacityUsedLocked(); got != want || got < 0 || m.maxEntries > 0 && got > m.maxEntries {
		t.Fatalf("capacity=%d records=%d limit=%d", got, want, m.maxEntries)
	}
	if m.maxEntries > 0 && (m.order == nil || m.order.Len() != len(m.items)) {
		t.Fatal("FIFO and map diverged")
	}
}

func exerciseMemoryCapacity(t *testing.T, operations []byte) {
	t.Helper()
	m, err := NewMemoryWithMaxEntries(4)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range operations {
		key := fmt.Sprintf("entry-%d", (op/12)%8)
		switch op % 12 {
		case 0:
			err = m.Set(key, int64(op), 0)
		case 1:
			err = m.Delete(key)
		case 2:
			_, err = m.Inc(key, 1)
		case 3:
			_, err = m.Dec(key, 1)
		case 4:
			err = m.Update(key, 0, func(interface{}, bool) (interface{}, bool, error) { return int64(op), false, nil })
		case 5:
			err = m.Update(key, 0, func(interface{}, bool) (interface{}, bool, error) { return nil, true, nil })
		case 6:
			err = m.Set(cacheFenceMetadataPrefix+key, int64(op), time.Second)
		case 7:
			err = m.Set(cacheTagMetadataPrefix+key, int64(op), time.Second)
		case 8:
			err = m.ClearPrefix("entry-")
		case 9:
			err = m.Clear()
		case 10:
			time.Sleep(time.Second)
			_, _, err = m.Get(key)
		case 11:
			time.Sleep(time.Minute)
			_, err = m.AcquireLock(key, "owner", time.Second)
		}
		if err != nil && !errors.Is(err, ErrMemoryCapacityExhausted) {
			t.Fatalf("op=%d: %v", op, err)
		}
		assertMemoryCapacityAccounting(t, m)
	}
}

func TestMemoryCapacityAccountingMutationPaths(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		operations := make([]byte, 1024)
		for i := range operations {
			operations[i] = byte(i*29 + 7)
		}
		exerciseMemoryCapacity(t, operations)
	})
}

func TestMemoryCapacityAccountingConcurrentWriters(t *testing.T) {
	m, err := NewMemoryWithMaxEntries(32)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				key := fmt.Sprintf("entry-%d-%d", worker, i%8)
				if err := m.Set(key, int64(i), 0); err != nil {
					t.Error(err)
					return
				}
				if i%3 == 0 {
					if err := m.Delete(key); err != nil {
						t.Error(err)
						return
					}
				}
				assertMemoryCapacityAccounting(t, m)
			}
		}()
	}
	wg.Wait()
	assertMemoryCapacityAccounting(t, m)
}

func FuzzMemoryCapacityAccounting(f *testing.F) {
	f.Add([]byte{0, 12, 24, 36, 48, 6, 7, 10, 5, 8, 9, 11})
	f.Add([]byte{7, 19, 31, 43, 55, 10, 1, 3, 4, 5})
	f.Fuzz(func(t *testing.T, operations []byte) {
		if len(operations) > 256 {
			return
		}
		synctest.Test(t, func(t *testing.T) { exerciseMemoryCapacity(t, operations) })
	})
}

func TestMemoryCapacityAccountingZeroValueAndErrors(t *testing.T) {
	for _, m := range []*Memory{{}, NewMemory()} {
		if err := m.Set("kept", int64(1), 0); err != nil {
			t.Fatal(err)
		}
		if err := m.Set("bad", 1, -time.Second); !errors.Is(err, ErrInvalidDriverTTL) {
			t.Fatal(err)
		}
		injected := errors.New("abort")
		if err := m.Update("kept", 0, func(interface{}, bool) (interface{}, bool, error) { return nil, true, injected }); !errors.Is(err, injected) {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := m.UpdateIfLockOwnerContext(ctx, "absent", 0, "lease", "owner", func(interface{}, bool) (interface{}, bool, error) {
			t.Error("cancelled callback ran")
			return 1, false, nil
		}); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		requireMemoryValue(t, m, "kept", int64(1))
		assertMemoryCapacityAccounting(t, m)
		if err := m.Delete("absent"); err != nil {
			t.Fatal(err)
		}
		if err := m.Set(cacheFenceMetadataPrefix+"persist", "fence", 0); err != nil {
			t.Fatal(err)
		}
		if err := m.Clear(); err != nil {
			t.Fatal(err)
		}
		assertMemoryCapacityAccounting(t, m)
		requireMemoryValue(t, m, cacheFenceMetadataPrefix+"persist", "fence")
		if err := m.Set("again", 2, 0); err != nil {
			t.Fatal(err)
		}
		assertMemoryCapacityAccounting(t, m)
	}
}
