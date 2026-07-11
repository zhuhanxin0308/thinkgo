package connector

import (
	"net/url"
	"testing"

	"thinkgo/framework/db"
)

// TestBuildMongoURIEscapesCredentials 验证 MongoDB URI 不会被账号、密码或库名中的特殊字符破坏。
func TestBuildMongoURIEscapesCredentials(t *testing.T) {
	uri := buildMongoURI(db.Config{
		Username: "mongo:user",
		Password: "p@ss/word?authSource=admin",
		Hostname: "mongo.internal",
		Hostport: "27017",
		Database: "think go",
		Params: map[string]string{
			"authSource": "admin",
		},
	})

	parsed, err := url.Parse(uri)
	if err != nil {
		t.Fatalf("MongoDB URI 应为可解析 URL，实际错误: %v", err)
	}
	password, _ := parsed.User.Password()
	if parsed.User.Username() != "mongo:user" || password != "p@ss/word?authSource=admin" {
		t.Fatalf("MongoDB URI 凭据解析错误: user=%q password=%q", parsed.User.Username(), password)
	}
	if parsed.Host != "mongo.internal:27017" || parsed.EscapedPath() != "/think%20go" {
		t.Fatalf("MongoDB URI 地址或库名解析错误: host=%q path=%q", parsed.Host, parsed.EscapedPath())
	}
	if parsed.Query().Get("authSource") != "admin" {
		t.Fatalf("MongoDB URI 应保留显式参数，实际 authSource=%q", parsed.Query().Get("authSource"))
	}
}
