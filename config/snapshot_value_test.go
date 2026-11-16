package config

import (
	"errors"
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
