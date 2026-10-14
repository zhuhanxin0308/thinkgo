package driver

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
	"unsafe"
)

func testBudgetMemory(t *testing.T, entries int, value, total uint64, locks int) *Memory {
	t.Helper()
	m, err := NewMemoryWithBudget(MemoryBudget{MaxEntries: entries, MaxValueBytes: value, MaxBytes: total, MaxLocks: locks})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func assertMemoryBudget(t *testing.T, m *Memory) {
	t.Helper()
	m.lock.RLock()
	defer m.lock.RUnlock()
	var charge uint64
	for _, value := range m.items {
		charge += value.bytes
	}
	for _, lease := range m.locks {
		charge += lease.bytes
	}
	if charge != m.budgetBytes || charge > m.budget.MaxBytes || len(m.items) > m.budget.MaxEntries || len(m.locks) > m.budget.MaxLocks {
		t.Fatalf("budget accounting: charged=%d ledger=%d items=%d locks=%d", charge, m.budgetBytes, len(m.items), len(m.locks))
	}
}

func TestMemoryBudgetValidation(t *testing.T) {
	for _, budget := range []MemoryBudget{
		{}, {MaxEntries: -1, MaxValueBytes: 1, MaxBytes: 2, MaxLocks: 1},
		{MaxEntries: 1, MaxValueBytes: 0, MaxBytes: 2, MaxLocks: 1},
		{MaxEntries: 1, MaxValueBytes: 3, MaxBytes: 2, MaxLocks: 1},
		{MaxEntries: 1, MaxValueBytes: 1, MaxBytes: 2, MaxLocks: 0},
	} {
		if _, err := NewMemoryWithBudget(budget); !errors.Is(err, ErrInvalidMemoryBudget) {
			t.Fatalf("accepted invalid budget: %v", err)
		}
	}
	// The constructor owns a value copy. Legacy constructors remain opt-in unlimited.
	budget := MemoryBudget{MaxEntries: 1, MaxValueBytes: 1024, MaxBytes: 2048, MaxLocks: 1}
	m, err := NewMemoryWithBudget(budget)
	if err != nil {
		t.Fatal(err)
	}
	budget.MaxEntries = 0
	if m.budget.MaxEntries != 1 || NewMemory().budget != nil {
		t.Fatal("budget copy or legacy contract changed")
	}
}

func TestMemoryBudgetGraphsAndOwnership(t *testing.T) {
	type namedPointer *int
	type namedBytes []byte
	type exported struct {
		Values []string
		number int
	}
	integer := 7
	inputs := []interface{}{
		nil, true, int8(-1), uint64(9), float64(2), complex128(3), "text",
		namedPointer(&integer), (*int)(nil), []int(nil), map[string]int(nil),
		namedBytes{1, 2}, [2]string{"a", "b"}, exported{Values: []string{"value"}, number: 4},
		map[string]interface{}{"a": []int{1, 2}, "b": nil},
	}
	for _, input := range inputs {
		clone, _, err := budgetSnapshot(input, 1<<20)
		if err != nil || !reflect.DeepEqual(clone, input) || reflect.TypeOf(clone) != reflect.TypeOf(input) {
			t.Fatalf("snapshot %T: %v %#v", input, err, clone)
		}
	}

	backing := strings.Repeat("x", 10000)
	piece := backing[100:110]
	clone, _, err := budgetSnapshot(piece, 100)
	if err != nil || unsafe.StringData(piece) == unsafe.StringData(clone.(string)) {
		t.Fatal("substring storage retained", err)
	}

	bytes := make([]byte, 2, 10000)
	graph := map[string]interface{}{"a": bytes, "b": bytes}
	graph["self"] = graph
	m := testBudgetMemory(t, 4, 1<<20, 2<<20, 4)
	if err := m.Set("graph", graph, 0); err != nil {
		t.Fatal(err)
	}
	bytes[0] = 9
	value, found, err := m.Get("graph")
	if err != nil || !found {
		t.Fatal(found, err)
	}
	got := value.(map[string]interface{})
	a, b := got["a"].([]byte), got["b"].([]byte)
	if a[0] != 0 || cap(a) != len(a) {
		t.Fatal("retained source or excess slice capacity")
	}
	a[0] = 4
	if b[0] != 4 {
		t.Fatal("shared reference was not preserved")
	}
	got["self"].(map[string]interface{})["extra"] = true
	if got["extra"] != true {
		t.Fatal("cycle was not preserved")
	}
	value, _, err = m.Get("graph")
	if err != nil || value.(map[string]interface{})["a"].([]byte)[0] != 0 {
		t.Fatal("read snapshot changed stored value", err)
	}
	assertMemoryBudget(t, m)
}

func TestMemoryBudgetRejectsUnaccountableGraphsBeforeCommit(t *testing.T) {
	type hidden struct{ value string }
	type pointerNode struct{ Next *pointerNode }
	integer := 0
	deep := &pointerNode{}
	for i := 0; i < 70; i++ {
		deep = &pointerNode{Next: deep}
	}
	unsupported := []interface{}{
		make(chan int), (func())(nil), unsafe.Pointer(&integer), uintptr(1),
		hidden{value: "hidden"}, time.Now(), map[*int]int{&integer: 1}, map[interface{}]int{1: 1}, map[float64]int{1: 1}, map[complex128]int{1: 1},
	}
	m := testBudgetMemory(t, 2, 2048, 4096, 2)
	if err := m.Set("existing", "keep", 0); err != nil {
		t.Fatal(err)
	}
	for _, value := range unsupported {
		if err := m.Set("existing", value, 0); !errors.Is(err, ErrUnsupportedMemoryValue) {
			t.Fatalf("accepted %T: %v", value, err)
		}
		requireMemoryValue(t, m, "existing", "keep")
		assertMemoryBudget(t, m)
	}
	for _, value := range []interface{}{strings.Repeat("x", 2048), make([]byte, 2048), map[string][4096]byte{"large": {}}, deep, make([]struct{}, 70000), [4096]byte{}} {
		if err := m.Set("existing", value, 0); !errors.Is(err, ErrMemoryValueTooLarge) {
			t.Fatalf("accepted large graph %T: %v", value, err)
		}
	}
	if _, _, err := budgetSnapshot(deep, 1<<20); !errors.Is(err, ErrMemoryValueTooLarge) {
		t.Fatal("depth budget bypass", err)
	}
}

func TestMemoryBudgetSharedDepthIsDeterministic(t *testing.T) {
	type node struct{ Next *node }
	for _, tailFirst := range []bool{false, true} {
		graph := make(map[string]interface{})
		var next *node
		for i := 0; i < 40; i++ {
			next = &node{Next: next}
			key := i
			if tailFirst {
				key = 39 - i
			}
			graph[fmt.Sprintf("%02d", key)] = next
		}
		m := testBudgetMemory(t, 2, 1<<20, 2<<20, 2)
		for i := 0; i < 100; i++ {
			err := m.Set("graph", graph, 0)
			if !tailFirst {
				if err != nil {
					t.Fatal("short canonical traversal should fit", err)
				}
				if _, found, err := m.Get("graph"); !found || err != nil {
					t.Fatal("admitted graph became unreadable", found, err)
				}
			} else if !errors.Is(err, ErrMemoryValueTooLarge) {
				t.Fatal("long canonical traversal should always reject", err)
			}
		}
	}
}

func TestMemoryBudgetOwnsOverwrittenKeysAndLockOwners(t *testing.T) {
	m := testBudgetMemory(t, 4, 1024, 4096, 2)
	backing := strings.Repeat("k", 10000)
	key := backing[:1]
	if err := m.Set("k", "first", 0); err != nil {
		t.Fatal(err)
	}
	if err := m.Set(key, "second", 0); err != nil {
		t.Fatal(err)
	}
	for storedKey := range m.items {
		if unsafe.StringData(storedKey) == unsafe.StringData(key) {
			t.Fatal("overwrite retained large key backing")
		}
	}
	ownerBacking := strings.Repeat("o", 10000)
	owner := ownerBacking[:1]
	if ok, err := m.AcquireLock(key, owner, time.Minute); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, err := m.RenewLock(key, owner, time.Minute); !ok || err != nil {
		t.Fatal(ok, err)
	}
	for storedKey, lease := range m.locks {
		if unsafe.StringData(storedKey) == unsafe.StringData(key) || unsafe.StringData(lease.owner) == unsafe.StringData(owner) {
			t.Fatal("lock retained external substring backing")
		}
	}
	shared := []byte{1, 2}
	withSharing := []interface{}{shared, shared}
	withoutSharing := []interface{}{[]byte{1, 2}, []byte{1, 2}}
	sharedCharge, err := budgetValueCharge(withSharing, 4096)
	if err != nil {
		t.Fatal(err)
	}
	separateCharge, err := budgetValueCharge(withoutSharing, 4096)
	if err != nil || sharedCharge >= separateCharge {
		t.Fatal("shared allocation should count once", sharedCharge, separateCharge, err)
	}
}

func TestMemoryBudgetOverwriteDeleteAndClear(t *testing.T) {
	m := testBudgetMemory(t, 5, 512, 800, 2)
	for _, key := range []string{"a", "b"} {
		if err := m.Set(key, "small", 0); err != nil {
			t.Fatal(err)
		}
	}
	large := strings.Repeat("x", 300)
	if err := m.Set("a", large, 0); !errors.Is(err, ErrMemoryCapacityExhausted) {
		t.Fatal("growth should fail total budget", err)
	}
	requireMemoryValue(t, m, "a", "small")
	assertMemoryBudget(t, m)
	if err := m.Delete("b"); err != nil {
		t.Fatal(err)
	}
	if err := m.Set("a", large, 0); err != nil {
		t.Fatal(err)
	}
	assertMemoryBudget(t, m)
	if err := m.Set("a", "", 0); err != nil {
		t.Fatal(err)
	}
	if err := m.Set("c", strings.Repeat("c", 200), 0); err != nil {
		t.Fatal("shrink did not release budget", err)
	}
	assertMemoryBudget(t, m)
	if err := m.ClearPrefix("c"); err != nil {
		t.Fatal(err)
	}
	if err := m.Clear(); err != nil {
		t.Fatal(err)
	}
	if m.budgetBytes != 0 {
		t.Fatal("clear leaked budget")
	}
	if err := m.Set(strings.Repeat("k", 801), nil, 0); !errors.Is(err, ErrMemoryCapacityExhausted) {
		t.Fatal("key bypassed budget", err)
	}
}

func TestMemoryBudgetFenceAndLockAdmission(t *testing.T) {
	m := testBudgetMemory(t, 1, 256, 512, 1)
	fence := cacheFenceMetadataPrefix + "scope"
	if err := m.Set(fence, uint64(1), 0); err != nil {
		t.Fatal(err)
	}
	if err := m.Set("business", nil, 0); !errors.Is(err, ErrMemoryCapacityExhausted) {
		t.Fatal("fence bypassed entry budget", err)
	}
	if err := m.Clear(); err != nil {
		t.Fatal(err)
	}
	if err := m.ClearPrefix(cacheFenceMetadataPrefix); err != nil {
		t.Fatal(err)
	}
	requireMemoryValue(t, m, fence, uint64(1))
	if ok, err := m.AcquireLock("lock", "owner", time.Minute); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, err := m.AcquireLock("second", "owner", time.Minute); ok || !errors.Is(err, ErrMemoryCapacityExhausted) {
		t.Fatal("lock count bypass", ok, err)
	}
	if ok, err := m.AcquireLock("lock", "other", time.Minute); ok || err != nil {
		t.Fatal("existing lock should stay busy", ok, err)
	}
	if ok, err := m.ReleaseLock("lock", "other"); ok || err != nil {
		t.Fatal("wrong owner released", ok, err)
	}
	if ok, err := m.RenewLock("lock", "other", time.Minute); ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, err := m.RenewLock("lock", "owner", time.Minute); !ok || err != nil {
		t.Fatal(ok, err)
	}
	assertMemoryBudget(t, m)
	if ok, err := m.ReleaseLock("lock", "owner"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, err := m.AcquireLock(strings.Repeat("k", 512), "owner", time.Minute); ok || !errors.Is(err, ErrMemoryCapacityExhausted) {
		t.Fatal("lock key bypassed bytes", ok, err)
	}
	if err := m.Delete(fence); err != nil {
		t.Fatal(err)
	}
	assertMemoryBudget(t, m)
}

func TestMemoryBudgetReclaimsExpiredDataAndLocks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := testBudgetMemory(t, 3, 256, 512, 1)
		if err := m.Set("expired", nil, time.Second); err != nil {
			t.Fatal(err)
		}
		if ok, err := m.AcquireLock("one", "owner", time.Second); !ok || err != nil {
			t.Fatal(ok, err)
		}
		time.Sleep(time.Second)
		if ok, err := m.AcquireLock(strings.Repeat("k", 250), "owner", time.Second); !ok || err != nil {
			t.Fatal("data expiration blocked lock", ok, err)
		}
		time.Sleep(time.Second)
		if err := m.Set("replacement", nil, 0); err != nil {
			t.Fatal("lock expiration blocked data", err)
		}
		assertMemoryBudget(t, m)
		if ok, err := m.AcquireLock("two", "owner", time.Second); !ok || err != nil {
			t.Fatal(ok, err)
		}
		time.Sleep(time.Second)
		if ok, err := m.RenewLock("two", "owner", time.Minute); ok || err != nil {
			t.Fatal(ok, err)
		}
		if ok, err := m.AcquireLock("three", "owner", time.Second); !ok || err != nil {
			t.Fatal(ok, err)
		}
		time.Sleep(time.Second)
		if ok, err := m.ReleaseLock("three", "owner"); ok || err != nil {
			t.Fatal(ok, err)
		}
		assertMemoryBudget(t, m)
	})
}

func TestMemoryBudgetAtomicAndConcurrentWrites(t *testing.T) {
	m := testBudgetMemory(t, 4, 256, 2048, 2)
	if err := m.Set("value", int64(1), 0); err != nil {
		t.Fatal(err)
	}
	bad := func(interface{}, bool) (interface{}, bool, error) { return make(chan int), false, nil }
	if err := m.Update("value", 0, bad); !errors.Is(err, ErrUnsupportedMemoryValue) {
		t.Fatal(err)
	}
	if err := m.UpdatePreserveTTL("value", bad); !errors.Is(err, ErrUnsupportedMemoryValue) {
		t.Fatal(err)
	}
	if err := m.UpdatePreserveTTLConditionally("value", func(interface{}, bool) (interface{}, bool, bool, error) { return "value", false, true, nil }); err != nil {
		t.Fatal(err)
	}
	if err := m.Set("value", int64(1), 0); err != nil {
		t.Fatal(err)
	}
	if ok, err := m.AcquireLock("lease", "owner", time.Minute); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, err := m.UpdateIfLockOwnerContext(context.Background(), "value", 0, "lease", "owner", bad); ok || !errors.Is(err, ErrUnsupportedMemoryValue) {
		t.Fatal(ok, err)
	}
	requireMemoryValue(t, m, "value", int64(1))
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := 0; j < 50; j++ {
				if _, err := m.Inc("value", 1); err != nil {
					t.Error(err)
				}
				if _, err := m.Dec("value", 1); err != nil {
					t.Error(err)
				}
				if _, _, err := m.Get("value"); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	workers.Wait()
	requireMemoryValue(t, m, "value", int64(1))
	assertMemoryBudget(t, m)
	if err := m.Update("value", 0, func(interface{}, bool) (interface{}, bool, error) { return nil, true, nil }); err != nil {
		t.Fatal(err)
	}
	assertMemoryBudget(t, m)
}
