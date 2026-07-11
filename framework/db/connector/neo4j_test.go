package connector

import (
	"testing"

	"thinkgo/framework/db"
)

// TestBuildNeo4jURIUsesSecureDefault 验证 Neo4j 默认使用加密协议，避免明文 Bolt 成为默认连接方式。
func TestBuildNeo4jURIUsesSecureDefault(t *testing.T) {
	uri, err := buildNeo4jURI(db.Config{
		Hostname: "neo4j.internal",
		Hostport: "7687",
	})
	if err != nil {
		t.Fatalf("默认 Neo4j URI 不应报错: %v", err)
	}
	if uri != "neo4j+s://neo4j.internal:7687" {
		t.Fatalf("默认 Neo4j URI 应使用 neo4j+s，实际为 %q", uri)
	}
}

// TestBuildNeo4jURIAllowsExplicitSchemeOverride 验证本地开发可显式降级协议。
func TestBuildNeo4jURIAllowsExplicitSchemeOverride(t *testing.T) {
	uri, err := buildNeo4jURI(db.Config{
		Hostname: "127.0.0.1",
		Hostport: "7687",
		Params: map[string]string{
			"scheme": "bolt",
		},
	})
	if err != nil {
		t.Fatalf("显式 Neo4j URI 协议不应报错: %v", err)
	}
	if uri != "bolt://127.0.0.1:7687" {
		t.Fatalf("Neo4j URI 应尊重显式协议，实际为 %q", uri)
	}
}

// TestBuildNeo4jURIRejectsUnsupportedScheme 验证不允许通过配置注入未知协议。
func TestBuildNeo4jURIRejectsUnsupportedScheme(t *testing.T) {
	if _, err := buildNeo4jURI(db.Config{Params: map[string]string{"scheme": "http"}}); err == nil {
		t.Fatal("Neo4j URI 应拒绝不受支持的协议")
	}
}
