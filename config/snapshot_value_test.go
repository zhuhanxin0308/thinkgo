package config

import (
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTypedSnapshotPreservesNilAndEmptyScalarSlices(t *testing.T) {
	for _, test := range []struct {
		name            string
		nilValue, empty interface{}
		populated       interface{}
		populatedJSON   string
	}{
		{"strings", []string(nil), []string{}, []string{"api"}, `["api"]`},
		{"ints", []int(nil), []int{}, []int{1}, `[1]`},
		{"int64s", []int64(nil), []int64{}, []int64{2}, `[2]`},
		{"floats", []float64(nil), []float64{}, []float64{1.5}, `[1.5]`},
		{"bools", []bool(nil), []bool{}, []bool{true}, `[true]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := NewConfig()
			values := []struct {
				key, json string
				input     interface{}
			}{
				{"nil", "null", test.nilValue},
				{"empty", "[]", test.empty},
				{"populated", test.populatedJSON, test.populated},
			}
			for _, item := range values {
				if err := cfg.Set("feature."+item.key, item.input); err != nil {
					t.Fatal(err)
				}
			}
			clone := cfg.Clone()
			replaced := NewConfig()
			replaced.ReplaceWithSnapshot(cfg)
			reflect.ValueOf(test.populated).Index(0).SetZero()
			for _, snapshot := range []*Config{cfg, clone, replaced} {
				group := snapshot.GetMap("feature")
				for _, item := range values {
					for _, got := range []interface{}{snapshot.Get("feature." + item.key), group[item.key]} {
						data, err := json.Marshal(got)
						if err != nil || string(data) != item.json || reflect.TypeOf(got) != reflect.TypeOf(item.input) {
							t.Errorf("快照 %s 的类型或 JSON 发生变化: %T %s, %v", item.key, got, data, err)
						}
					}
				}
				reflect.ValueOf(group["populated"]).Index(0).SetZero()
				if data, _ := json.Marshal(snapshot.Get("feature.populated")); string(data) != test.populatedJSON {
					t.Errorf("GetMap 返回值不应修改内部存储: %s", data)
				}
			}
		})
	}
}

// TestTypedSnapshotPreservesDynamicCollections 验证空集合与 null 在读写和快照中保留各自语义。
func TestTypedSnapshotPreservesDynamicCollections(t *testing.T) {
	cfg := NewConfig()
	file := filepath.Join(t.TempDir(), "feature.json")
	if err := os.WriteFile(file, []byte(`{"from_file":null}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Load(file, "feature"); err != nil {
		t.Fatal(err)
	}
	values := []struct {
		key, json string
		input     interface{}
	}{
		{"nil_map", "null", map[string]interface{}(nil)},
		{"empty_map", "{}", map[string]interface{}{}},
		{"map", `{"inner":null}`, map[string]interface{}{"INNER": []interface{}(nil)}},
		{"nil_list", "null", []interface{}(nil)},
		{"empty_list", "[]", []interface{}{}},
		{"list", `[null]`, []interface{}{map[string]interface{}(nil)}},
		{"nil_string_map", "null", map[string]string(nil)},
	}
	for _, item := range values {
		if err := cfg.Set("feature."+item.key, item.input); err != nil {
			t.Fatal(err)
		}
	}
	clone := cfg.Clone()
	replaced := NewConfig()
	replaced.ReplaceWithSnapshot(clone)
	for _, current := range []*Config{cfg, clone, replaced} {
		group := current.GetMap("feature")
		for _, item := range values {
			path := "feature." + item.key
			if !current.Has(path) {
				t.Errorf("已设置的路径不可见: %s", path)
			}
			for _, value := range []interface{}{current.Get(path), group[item.key]} {
				data, err := json.Marshal(value)
				if err != nil || string(data) != item.json || reflect.TypeOf(value) != reflect.TypeOf(item.input) {
					t.Errorf("配置 %s 应为 %T、JSON %s，实际为 %T、%s，错误 %v", path, item.input, item.json, value, data, err)
				}
			}
		}
		if group["from_file"] != nil || !current.Has("feature.from_file") || current.GetMap("feature.nil_map") != nil {
			t.Error("配置文件 null 和强类型 nil map 应保留空值")
		}
		if err := current.Set("feature.nil_map.child", true); err != nil {
			t.Fatal(err)
		}
		if !current.GetBool("feature.nil_map.child", false) {
			t.Error("应能在原 nil map 下写入子节点")
		}
	}
	for _, loadAll := range []bool{false, true} {
		if err := cfg.Set("feature", map[string]interface{}(nil)); err != nil {
			t.Fatal(err)
		}
		var err error
		if loadAll {
			err = cfg.LoadAll(filepath.Dir(file))
		} else {
			err = cfg.Load(file, "feature")
		}
		if err != nil || !cfg.Has("feature.from_file") {
			t.Fatalf("文件配置应能覆盖 nil 命名空间，批量加载=%t，错误 %v", loadAll, err)
		}
	}
}

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

func TestDynamicMapAllowsIdenticalSharedSlicesRejectsCyclesAndBoundsDiamondDAG(t *testing.T) {
	items := []int{1, 2, 3}
	sharedList := []interface{}{"x", "y"}
	cfg := NewConfig()

	if err := cfg.Set("map_holder", map[string]interface{}{"a": items, "b": items, "la": sharedList, "lb": sharedList}); err != nil {
		t.Fatalf("动态 map 中合法共享同一切片头不应报错: %v", err)
	}
	a := cfg.Get("map_holder.a").([]int)
	b := cfg.Get("map_holder.b").([]int)
	a[0] = 99
	if b[0] != 1 || items[0] != 1 {
		t.Fatalf("动态分支之间的深拷贝未彼此隔离: a=%v b=%v items=%v", a, b, items)
	}
	if err := cfg.Set("map_holder.a", []int{9, 9}); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Get("map_holder.b").([]int); got[0] != 1 {
		t.Fatalf("修改一个动态分支影响了另一个共享来源分支: %v", got)
	}

	if err := cfg.Set("overlap", map[string]interface{}{"a": items[0:2], "b": items[1:3]}); !errors.Is(err, ErrConfigUnsupportedValue) {
		t.Fatalf("部分错位重叠切片仍必须被拒绝: %v", err)
	}

	selfMap := map[string]interface{}{}
	selfMap["self"] = selfMap
	if err := cfg.Set("self_map", selfMap); !errors.Is(err, ErrConfigUnsupportedValue) || errors.Is(err, ErrConfigJSONTooDeep) {
		t.Fatalf("自引用动态 map 应返回 ErrConfigUnsupportedValue，实际为 %v", err)
	}

	selfSlice := make([]interface{}, 1)
	selfSlice[0] = selfSlice
	if err := cfg.Set("self_slice", selfSlice); !errors.Is(err, ErrConfigUnsupportedValue) || errors.Is(err, ErrConfigJSONTooDeep) {
		t.Fatalf("自引用动态 slice 应返回 ErrConfigUnsupportedValue，实际为 %v", err)
	}

	// 构造 35 层无环菱形共享 map[string]any（展开树达 2^35 节点），验证节点预算快速受控拒绝而不发生指数级 OOM。
	leaf := map[string]interface{}{"v": 1}
	current := leaf
	for range 35 {
		current = map[string]interface{}{"left": current, "right": current}
	}
	start := time.Now()
	if err := cfg.Set("diamond", current); !errors.Is(err, ErrConfigUnsupportedValue) {
		t.Fatalf("超大规模共享菱形 DAG 应受节点预算拦截并返回 ErrConfigUnsupportedValue，实际为 %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("35 层共享菱形 DAG 处理耗时过长: %s", elapsed)
	}
}
