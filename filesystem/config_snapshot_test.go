package filesystem

import (
	"bytes"
	"reflect"
	"sync"
	"testing"
)

type snapshotNames []string
type snapshotLabels map[string]string

// TestFilesystemTypedConfigIsolation 覆盖输入、两个读取入口及工厂重建之间的集合所有权。
func TestFilesystemTypedConfigIsolation(t *testing.T) {
	for _, test := range []struct {
		name   string
		value  interface{}
		want   interface{}
		mutate func(interface{})
	}{
		{"字符串切片", []string{"original"}, []string{"original"}, func(value interface{}) { value.([]string)[0] = "changed" }},
		{"整数切片", []int{1}, []int{1}, func(value interface{}) { value.([]int)[0] = 2 }},
		{"强类型映射", map[string]string{"key": "original"}, map[string]string{"key": "original"}, func(value interface{}) { value.(map[string]string)["key"] = "changed" }},
		{"命名切片", snapshotNames{"original"}, snapshotNames{"original"}, func(value interface{}) { value.(snapshotNames)[0] = "changed" }},
		{"命名映射", snapshotLabels{"key": "original"}, snapshotLabels{"key": "original"}, func(value interface{}) { value.(snapshotLabels)["key"] = "changed" }},
		{"嵌套映射", map[string][]int{"key": {1}}, map[string][]int{"key": {1}}, func(value interface{}) { value.(map[string][]int)["key"][0] = 2 }},
		{"数组嵌套切片", [1][]string{{"original"}}, [1][]string{{"original"}}, func(value interface{}) { value.([1][]string)[0][0] = "changed" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := New(map[string]interface{}{
				"default": "custom",
				"disks": map[string]interface{}{
					"custom": map[string]interface{}{"type": "custom", "options": test.value},
				},
			})
			t.Cleanup(func() { _ = manager.Close() })
			test.mutate(test.value)
			assertValue := func(value interface{}) {
				t.Helper()
				if !reflect.DeepEqual(value, test.want) {
					t.Errorf("配置被外部集合修改或类型丢失: got=%#v want=%#v", value, test.want)
				}
			}
			assertValue(manager.GetConfig("disks.custom.options"))
			snapshot, err := manager.GetDiskConfig("custom", "options")
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(snapshot)
			assertValue(manager.GetConfig("disks.custom.options"))
			test.mutate(manager.GetConfig().(map[string]interface{})["disks"].(map[string]interface{})["custom"].(map[string]interface{})["options"])
			created := 0
			if err := manager.Extend("custom", func(configuration map[string]interface{}) (Driver, error) {
				created++
				assertValue(configuration["options"])
				test.mutate(configuration["options"])
				return &countingFilesystemDriver{}, nil
			}); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if _, err := manager.Disk(); err != nil {
					t.Fatal(err)
				}
				manager.ForgetDriver()
			}
			if created != 2 {
				t.Fatalf("未实际重建驱动: %d", created)
			}
			assertValue(manager.GetConfig("disks.custom.options"))
		})
	}
}

// TestFilesystemConfigGraph 验证循环、共享视图、nil 及不透明资源句柄的边界。
func TestFilesystemConfigGraph(t *testing.T) {
	shared := []string{"original", "second"}
	cycle := make([]interface{}, 2)
	cycle[0], cycle[1] = cycle, shared
	var nilMap map[string]string
	var nilSlice []string
	reader := bytes.NewBufferString("resource")
	configuration := map[string]interface{}{
		"cycle": cycle, "shared": shared, "short": shared[:1],
		"nilMap": nilMap, "nilSlice": nilSlice, "empty": []string{},
		"scalarArray": [2]int{1, 2}, "nestedArray": [1][1][]string{{shared}},
		"nil": nil, "reader": reader,
	}
	configuration["self"] = configuration
	manager := New(configuration)
	view := manager.GetConfig().(map[string]interface{})
	view["self"].(map[string]interface{})["marker"] = "snapshot"
	if view["marker"] != "snapshot" || manager.GetConfig("marker") != nil {
		t.Fatal("映射循环未保留或与管理器共享")
	}
	view["cycle"].([]interface{})[0].([]interface{})[1].([]string)[0] = "snapshot"
	if view["shared"].([]string)[0] != "snapshot" || shared[0] != "original" || manager.GetConfig("shared").([]string)[0] != "original" {
		t.Fatal("循环中的集合引用未隔离或共享关系丢失")
	}
	if len(view["short"].([]string)) != 1 || len(view["shared"].([]string)) != 2 {
		t.Fatal("共享起点的不同长度切片被错误合并")
	}
	if view["nil"] != nil || view["nilMap"].(map[string]string) != nil || view["nilSlice"].([]string) != nil || view["empty"].([]string) == nil {
		t.Fatal("nil 和空集合的语义被改变")
	}
	if view["reader"] != reader || view["scalarArray"] != [2]int{1, 2} {
		t.Fatal("资源身份或普通数组值被改变")
	}
	view["nestedArray"].([1][1][]string)[0][0][0] = "array"
	if manager.GetConfig("nestedArray").([1][1][]string)[0][0][0] != "original" {
		t.Fatal("嵌套数组的集合元素未隔离")
	}
}

// TestFilesystemConfigConcurrentSnapshots 验证修改各自返回的副本不竞争管理器内部数据。
func TestFilesystemConfigConcurrentSnapshots(t *testing.T) {
	manager := New(map[string]interface{}{"values": []string{"original"}})
	var workers sync.WaitGroup
	const readers = 8
	for range readers {
		workers.Go(func() {
			for range 100 {
				values := manager.GetConfig("values").([]string)
				if values[0] != "original" {
					t.Error("并发读取受到其他返回副本影响")
				}
				values[0] = "modified"
			}
		})
	}
	workers.Wait()
}

// TestFilesystemConfigNilCompatibility 保留旧动态容器可写空副本与强类型 nil 的区别。
func TestFilesystemConfigNilCompatibility(t *testing.T) {
	var dynamicMap map[string]interface{}
	var dynamicSlice []interface{}
	manager := New(map[string]interface{}{"map": dynamicMap, "slice": dynamicSlice})
	if manager.GetConfig("map").(map[string]interface{}) == nil || manager.GetConfig("slice").([]interface{}) == nil {
		t.Fatal("既有动态容器的可写空副本变成了 nil")
	}
	manager.GetConfig("map").(map[string]interface{})["added"] = "snapshot"
	if len(manager.GetConfig("map").(map[string]interface{})) != 0 {
		t.Fatal("空映射快照与内部配置共享")
	}
}
