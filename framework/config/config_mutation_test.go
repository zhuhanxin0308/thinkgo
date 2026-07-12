package config

import (
	"fmt"
	"sync"
	"testing"
)

// TestGetMapReturnsIsolatedSnapshot 验证 GetMap 返回独立快照，调用方修改不得污染共享配置。
func TestGetMapReturnsIsolatedSnapshot(t *testing.T) {
	c := NewConfig()
	c.Set("app", map[string]interface{}{
		"server": map[string]interface{}{
			"host":    "127.0.0.1",
			"aliases": []interface{}{"local", map[string]interface{}{"name": "primary"}},
		},
	})

	first := c.GetMap("app.server")
	first["host"] = "mutated"
	first["aliases"].([]interface{})[0] = "changed"
	first["aliases"].([]interface{})[1].(map[string]interface{})["name"] = "changed"
	second := c.GetMap("app.server")

	if second["host"] != "127.0.0.1" {
		t.Fatalf("修改 GetMap 快照不应污染内部配置，实际 host=%v", second["host"])
	}
	aliases := second["aliases"].([]interface{})
	if aliases[0] != "local" || aliases[1].(map[string]interface{})["name"] != "primary" {
		t.Fatalf("嵌套切片或 map 未被深拷贝，实际 aliases=%#v", aliases)
	}
}

// TestGetReturnsIsolatedRootAndCachedValues 验证根配置和点路径缓存命中均不暴露内部引用。
func TestGetReturnsIsolatedRootAndCachedValues(t *testing.T) {
	c := NewConfig()
	c.Set("app.server", map[string]interface{}{"hosts": []string{"a.example", "b.example"}})

	root := c.Get("").(map[string]interface{})
	root["app"].(map[string]interface{})["server"].(map[string]interface{})["hosts"].([]string)[0] = "root-mutated"

	first := c.Get("app.server").(map[string]interface{})
	first["hosts"].([]string)[0] = "cache-mutated"
	second := c.Get("app.server").(map[string]interface{})
	if got := second["hosts"].([]string)[0]; got != "a.example" {
		t.Fatalf("Get 返回值修改不应污染根配置或缓存值，实际为 %q", got)
	}
}

// TestGetMapCopyIsolatesMutation 验证兼容方法 GetMapCopy 同样返回递归隔离的快照。
func TestGetMapCopyIsolatesMutation(t *testing.T) {
	c := NewConfig()
	c.Set("app", map[string]interface{}{
		"server": map[string]interface{}{
			"host": "127.0.0.1",
			"tls":  map[string]interface{}{"enable": false},
		},
	})

	cp := c.GetMapCopy("app.server")
	cp["host"] = "0.0.0.0"
	cp["tls"].(map[string]interface{})["enable"] = true

	if got := c.GetString("app.server.host"); got != "127.0.0.1" {
		t.Fatalf("修改 GetMapCopy 快照不应影响内部配置，实际 host=%q", got)
	}
	if got := c.GetBool("app.server.tls.enable", false); got {
		t.Fatal("修改 GetMapCopy 中的嵌套 map 不应影响内部配置")
	}
}

// TestSetCopiesIncomingMap 验证 Set 会隔离调用方传入的嵌套 map。
func TestSetCopiesIncomingMap(t *testing.T) {
	c := NewConfig()
	source := map[string]interface{}{
		"server": map[string]interface{}{
			"host": "127.0.0.1",
		},
	}

	c.Set("app", source)
	source["server"].(map[string]interface{})["host"] = "0.0.0.0"

	if got := c.GetString("app.server.host"); got != "127.0.0.1" {
		t.Fatalf("Set 应复制传入 map，实际 host=%q", got)
	}
}

// TestSetCopiesIncomingStringSlice 验证 Set 会隔离调用方传入的原生字符串切片。
func TestSetCopiesIncomingStringSlice(t *testing.T) {
	c := NewConfig()
	hosts := []string{"example.com", "api.example.com"}

	c.Set("app.server.allowed_hosts", hosts)
	hosts[0] = "mutated.example.com"

	got, ok := c.Get("app.server.allowed_hosts").([]string)
	if !ok {
		t.Fatalf("allowed_hosts 应保持 []string 类型，实际为 %#v", c.Get("app.server.allowed_hosts"))
	}
	if got[0] != "example.com" {
		t.Fatalf("Set 应复制传入 []string，实际首项为 %q", got[0])
	}
}

// TestSetNestedDottedPathCreatesSubtree 验证点路径 Set 会创建缺失子树且保留同级配置。
func TestSetNestedDottedPathCreatesSubtree(t *testing.T) {
	c := NewConfig()
	c.Set("app", map[string]interface{}{"server": map[string]interface{}{"host": "127.0.0.1"}})

	c.Set("app.server.tls.enable", true)
	c.Set("app.server.port", "9000")

	if !c.GetBool("app.server.tls.enable", false) {
		t.Fatal("点路径 Set 应创建缺失的 tls 子树")
	}
	if got := c.GetString("app.server.port"); got != "9000" {
		t.Fatalf("点路径 Set 覆盖 port 失败，实际为 %q", got)
	}
	if got := c.GetString("app.server.host"); got != "127.0.0.1" {
		t.Fatalf("点路径 Set 不应破坏同级字段，实际 host=%q", got)
	}
}

// TestConfigConcurrentSnapshots 验证并发更新期间读取到的始终是完整、可独立修改的快照。
func TestConfigConcurrentSnapshots(t *testing.T) {
	c := NewConfig()
	c.Set("service", map[string]interface{}{"version": 0, "items": []interface{}{0, 0}})

	const loops = 200
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 1; i <= loops; i++ {
			c.Set("service", map[string]interface{}{"version": i, "items": []interface{}{i, i}})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < loops; i++ {
			snapshot := c.GetMap("service")
			items, ok := snapshot["items"].([]interface{})
			if !ok || len(items) != 2 || fmt.Sprint(items[0]) != fmt.Sprint(items[1]) {
				t.Errorf("读取到不完整配置快照：%#v", snapshot)
				return
			}
			items[0] = "local-only"
		}
	}()
	wg.Wait()

	items := c.GetMap("service")["items"].([]interface{})
	if items[0] == "local-only" {
		t.Fatal("并发读取方修改快照不应写回共享配置")
	}
}
