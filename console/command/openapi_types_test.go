package command

import (
	"context"
	"strings"
	"testing"
)

// TestOpenAPICommentsInheritNamedTypes 验证同模块跨包别名、派生结构体和泛型基础类型保留字段说明。
func TestOpenAPICommentsInheritNamedTypes(t *testing.T) {
	_, base := newOpenAPICommandFixture(t)
	writeDiscoveryFixture(t, base, "domain/entities/user.go", `package records

// User 用户资料。
type User struct {
	// Name 用户名。
	Name string
}

// Envelope 通用结果。
type Envelope[T any] struct {
	// Data 业务数据。
	Data T
}
`)
	writeDiscoveryFixture(t, base, "app/index/api/derived.go", `package api

import "example.com/project/domain/entities"

type Alias = records.User
// Derived 后台用户资料。
type Derived Alias
type Page records.Envelope[Derived]
`)
	source, err := scanOpenAPIComments(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	prefix := "example.com/project/app/index/api."
	if derived := source.Types[prefix+"Derived"]; derived.Description != "后台用户资料。" || derived.Fields["Name"] != "用户名。" {
		t.Fatalf("派生类型丢失说明: %#v", derived)
	}
	if page := source.Types[prefix+"Page"]; page.Fields["Data"] != "业务数据。" {
		t.Fatalf("泛型基础字段丢失说明: %#v", page)
	}
}

// TestOpenAPICommentsRejectTypeCycles 验证无效的派生类型循环给出源码位置，不无限递归。
func TestOpenAPICommentsRejectTypeCycles(t *testing.T) {
	_, base := newOpenAPICommandFixture(t)
	writeDiscoveryFixture(t, base, "app/index/api/cycle.go", "package api\ntype First Second\ntype Second First\n")
	if _, err := scanOpenAPIComments(context.Background(), base); err == nil || !strings.Contains(err.Error(), "cycle.go:") {
		t.Fatalf("类型循环缺少定位: %v", err)
	}
}
