package config

import (
	"errors"
	"math/big"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type snapshotEndpoint struct {
	Headers map[string]string
	Hosts   []string
	SeenAt  time.Time
	port    int
}

type snapshotFeature struct {
	Settings map[string]*snapshotEndpoint
	Ordered  []*snapshotEndpoint
}

func TestTypedSnapshotIsolatesStructuresPointersAndAliases(t *testing.T) {
	endpoint := &snapshotEndpoint{
		Headers: map[string]string{"X-Tenant": "original"},
		Hosts:   []string{"api.example.test"},
		SeenAt:  time.Now(),
		port:    8443,
	}
	input := snapshotFeature{
		Settings: map[string]*snapshotEndpoint{"primary": endpoint},
		Ordered:  []*snapshotEndpoint{endpoint},
	}
	configuration := NewConfig()
	if err := configuration.Set("feature.current", input); err != nil {
		t.Fatal(err)
	}
	clone := configuration.Clone()
	replaced := NewConfig()
	replaced.ReplaceWithSnapshot(clone)
	endpoint.Headers["X-Tenant"] = "source mutation"
	endpoint.Hosts[0] = "source mutation"

	for _, snapshot := range []*Config{clone, configuration, replaced} {
		value := snapshot.Get("feature.current").(snapshotFeature)
		got := value.Settings["primary"]
		if got == endpoint || got != value.Ordered[0] || got.Headers["X-Tenant"] != "original" ||
			got.Hosts[0] != "api.example.test" || got.port != endpoint.port || got.SeenAt != endpoint.SeenAt {
			t.Fatalf("强类型快照未隔离或改变了值: %+v", value)
		}
		got.Headers["X-Tenant"] = "snapshot mutation"
		got.Hosts[0] = "snapshot mutation"
	}
	value := configuration.Get("feature.current").(snapshotFeature)
	if got := value.Ordered[0].Headers["X-Tenant"]; got != "original" {
		t.Fatalf("Get 返回值仍可修改存储: %q", got)
	}
}

func TestTypedSnapshotRejectsNonCopyableValuesAtomically(t *testing.T) {
	configuration := NewConfig()
	if err := configuration.Set("feature", "original"); err != nil {
		t.Fatal(err)
	}
	type node struct{ Next *node }
	cycle := &node{}
	cycle.Next = cycle
	type recursiveMap map[string]recursiveMap
	mapCycle := make(recursiveMap)
	mapCycle["cycle"] = mapCycle
	private := struct{ headers map[string]string }{headers: map[string]string{"X-Tenant": "example"}}
	key := 1
	f, err := os.CreateTemp(t.TempDir(), "handle")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	for _, value := range []interface{}{
		private,
		&struct{ Lock sync.Mutex }{},
		&struct{ Count atomic.Int32 }{},
		&struct{ once sync.Once }{},
		f,
		make(chan int),
		map[*int]string{&key: "value"},
		cycle,
		mapCycle,
	} {
		if err := configuration.Set("feature", map[string]interface{}{"invalid": value}); !errors.Is(err, ErrConfigUnsupportedValue) {
			t.Fatalf("不安全的 %T 应得到明确错误: %v", value, err)
		}
		if got := configuration.GetString("feature"); got != "original" {
			t.Fatalf("失败写入改变了原有配置: %q", got)
		}
	}
}

func TestTypedSnapshotConcurrentReadersOwnIndependentValues(t *testing.T) {
	configuration := NewConfig()
	if err := configuration.Set("current", &snapshotEndpoint{
		Headers: map[string]string{"X-Tenant": "original"},
		Hosts:   []string{"api.example.test"},
	}); err != nil {
		t.Fatal(err)
	}
	var readers sync.WaitGroup
	for range 8 {
		readers.Go(func() {
			for range 20 {
				snapshot := configuration.Clone().Get("current").(*snapshotEndpoint)
				if snapshot.Headers["X-Tenant"] != "original" || snapshot.Hosts[0] != "api.example.test" {
					t.Error("并发读取得到被其他快照修改的值")
					return
				}
				snapshot.Headers["X-Tenant"] = "changed"
				snapshot.Hosts[0] = "changed"
			}
		})
	}
	readers.Wait()
}

func TestTypedSnapshotRejectsSharedBranchesThatExceedDepth(t *testing.T) {
	type node struct{ Next *node }
	chain := func(length int, tail *node) *node {
		for range length {
			tail = &node{Next: tail}
		}
		return tail
	}
	short := chain(10, nil)
	long := chain(maxConfigJSONDepth/2-5, short)
	configuration := NewConfig()
	value := struct {
		Short *node
		Long  *node
	}{short, long}
	if err := configuration.Set("deep", value); !errors.Is(err, ErrConfigUnsupportedValue) {
		t.Fatalf("短路径共享节点不能跳过长路径的深度检查: %v", err)
	}
	for range maxConfigJSONDepth {
		if err := configuration.Set("deep", map[string]*node{"short": short, "long": long}); !errors.Is(err, ErrConfigUnsupportedValue) {
			t.Fatalf("深度检查不应取决于 map 遍历顺序: %v", err)
		}
	}
}

func TestTypedSnapshotRejectsOverlappingStorageWithoutMutation(t *testing.T) {
	configuration := NewConfig()
	if err := configuration.Set("value", "original"); err != nil {
		t.Fatal(err)
	}
	items := []int{1, 2}
	type entry struct{ Number int }
	record := &entry{Number: 3}
	for _, value := range []interface{}{
		struct {
			Items    []int
			Selected *int
		}{items, &items[0]},
		struct {
			Items  []int
			Prefix []int
		}{items, items[:1]},
		struct {
			Record *entry
			Field  *int
		}{record, &record.Number},
		map[string]interface{}{"items": items, "selected": &items[0]},
	} {
		if err := configuration.Set("value", value); !errors.Is(err, ErrConfigUnsupportedValue) {
			t.Fatalf("无法保留别名关系的重叠存储应被拒绝: %v", err)
		}
		if got := configuration.GetString("value"); got != "original" {
			t.Fatalf("失败写入改变了配置: %q", got)
		}
	}
	adjacent := struct{ Left, Right []int }{items[:1:1], items[1:2:2]}
	if err := configuration.Set("value", adjacent); err != nil {
		t.Fatalf("可独立复制的不重叠视图应保持可用: %v", err)
	}
	identical := struct{ A, B []int }{items, items}
	if err := configuration.Set("value", identical); err != nil {
		t.Fatalf("完全相同的视图应复用副本: %v", err)
	}
	snapshot := configuration.Get("value").(struct{ A, B []int })
	snapshot.A[0] = 10
	if snapshot.B[0] != 10 || items[0] != 1 {
		t.Fatal("快照未保留独立副本内完全相同的视图")
	}
}

func TestApplyEnvironmentRejectsUnsafeTypedSnapshotBeforeCommit(t *testing.T) {
	configuration := NewConfig()
	if err := configuration.Set("numbers", []*big.Int{}); err != nil {
		t.Fatal(err)
	}
	if err := configuration.Set("name", "original"); err != nil {
		t.Fatal(err)
	}
	if err := configuration.ApplyEnvironment(environmentStub{
		"NAME":    "replacement",
		"NUMBERS": "[42]",
	}); !errors.Is(err, ErrConfigUnsupportedValue) {
		t.Fatalf("环境变量不能提交无法复制的强类型对象: %v", err)
	}
	if got := configuration.Get("numbers").([]*big.Int); len(got) != 0 || configuration.GetString("name") != "original" {
		t.Fatal("错误的环境覆盖改变了配置")
	}
}
