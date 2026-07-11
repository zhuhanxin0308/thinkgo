package config

import "testing"

// TestGetMapReturnsSharedReference 明确 GetMap 返回内部引用（契约：只读）。
func TestGetMapReturnsSharedReference(t *testing.T) {
	c := NewConfig()
	c.Set("app", map[string]interface{}{"server": map[string]interface{}{"host": "127.0.0.1"}})

	first := c.GetMap("app.server")
	second := c.GetMap("app.server")
	first["host"] = "mutated"

	if second["host"] != "mutated" {
		t.Fatal("GetMap 应返回同一内部引用（契约为只读）")
	}
}

// TestGetMapCopyIsolatesMutation 验证 GetMapCopy 返回深拷贝，修改副本不影响内部配置。
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

	// 内部配置不应被副本的修改影响。
	if got := c.GetString("app.server.host"); got != "127.0.0.1" {
		t.Fatalf("修改 GetMapCopy 副本不应影响内部配置，host 实际为 %q", got)
	}
	if got := c.GetBool("app.server.tls.enable", false); got != false {
		t.Fatal("修改 GetMapCopy 副本的嵌套 map 不应影响内部配置")
	}
}

// TestSetCopiesIncomingMap 验证 Set 会隔离调用方传入的 map，避免后续外部修改污染配置。
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
		t.Fatalf("Set 应复制传入 map，避免外部修改污染配置，实际 host=%q", got)
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
		t.Fatalf("Set 应复制传入 []string，实际第一个值为 %q", got[0])
	}
}

// TestSetNestedDottedPathCreatesSubtree 验证点路径 Set 能创建缺失的嵌套子树，
// 这是 app.go 用 Set("app.server.tls.enable", ...) 覆盖环境变量的前提。
func TestSetNestedDottedPathCreatesSubtree(t *testing.T) {
	c := NewConfig()
	c.Set("app", map[string]interface{}{"server": map[string]interface{}{"host": "127.0.0.1"}})

	// tls 子树原本不存在，Set 应自动创建。
	c.Set("app.server.tls.enable", true)
	c.Set("app.server.port", "9000")

	if !c.GetBool("app.server.tls.enable", false) {
		t.Fatal("点路径 Set 应创建缺失的 tls 子树并写入 enable")
	}
	if got := c.GetString("app.server.port"); got != "9000" {
		t.Fatalf("点路径 Set 覆盖 port 失败，实际 %q", got)
	}
	// 原有字段不应被破坏。
	if got := c.GetString("app.server.host"); got != "127.0.0.1" {
		t.Fatalf("点路径 Set 不应破坏同级已有字段，host 实际 %q", got)
	}
}
