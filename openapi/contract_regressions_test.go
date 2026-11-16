package openapi

import (
	"errors"
	"net/http"
	"strconv"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/zhuhanxin0308/thinkgo/v3/binding"
	"github.com/zhuhanxin0308/thinkgo/v3/route"
)

// TestRegisterTypedStringKeepsPlainTextContract 验证独立登记仍支持现有的纯文本客户端。
func TestRegisterTypedStringKeepsPlainTextContract(t *testing.T) {
	registry, err := NewRegistry(openapi3.Info{Title: "文本", Version: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterTyped[struct{}, string](registry, http.MethodGet, "/text", "text", http.StatusOK); err != nil {
		t.Fatal(err)
	}
	response := registry.document.Paths.Value("/text").Get.Responses.Status(http.StatusOK)
	if response == nil || response.Value == nil || len(response.Value.Content) != 1 || response.Value.Content["text/plain"] == nil {
		t.Fatalf("独立字符串契约不再声明纯文本: %#v", response)
	}
}

// TestWeakETagComparisonIsSymmetric 验证两侧的弱标签都按不透明值匹配。
func TestWeakETagComparisonIsSymmetric(t *testing.T) {
	for _, server := range []string{`"v1"`, `W/"v1"`} {
		for _, client := range []string{`"v1"`, `W/"v1"`} {
			if !matchesIfNoneMatch([]string{`"other"`, client}, server) {
				t.Errorf("未匹配等价标签，客户端=%s 服务端=%s", client, server)
			}
			if matchesIfNoneMatch([]string{client, "invalid"}, server) {
				t.Errorf("含非法成员的条件列表不应匹配，客户端=%s 服务端=%s", client, server)
			}
		}
	}
}

// TestTypedRegistryRejectsWhitespaceAndControlPaths 验证非法路径不会占用注册状态。
func TestTypedRegistryRejectsWhitespaceAndControlPaths(t *testing.T) {
	registry, err := NewRegistry(openapi3.Info{Title: "契约", Version: "1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/user name", "/user\tname", "/user\x01name", "/user\x7fname", "/user\u0085name"} {
		if err := RegisterTyped[struct{}, string](registry, http.MethodGet, path, "user", http.StatusOK); !errors.Is(err, ErrUnsupportedRoute) {
			t.Errorf("非法路径 %q 未被拒绝: %v", path, err)
		}
	}
	if err := RegisterTyped[struct{}, string](registry, http.MethodGet, "/users/上海", "user", http.StatusOK); err != nil {
		t.Fatalf("合法中文路径被拒绝或注册状态已污染: %v", err)
	}
}

// TestTypedRegistryRejectsContentForBodylessStatus 验证注册契约与 JSON 处理器的无正文约束一致。
func TestTypedRegistryRejectsContentForBodylessStatus(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusResetContent} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			registry, err := NewRegistry(openapi3.Info{Title: "契约", Version: "1"})
			if err != nil {
				t.Fatal(err)
			}
			if err := RegisterTyped[struct{}, string](registry, http.MethodGet, "/reset", "reset", status); !errors.Is(err, binding.ErrDefinition) {
				t.Fatalf("无正文状态接受了类型化响应: %v", err)
			}
			if err := Handle(route.NewRouter(), registry, Operation{Method: http.MethodGet, Path: "/reset", OperationID: "reset", SuccessStatus: status}, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			response := registry.document.Paths.Value("/reset").Get.Responses.Status(status)
			if response == nil || response.Value == nil || len(response.Value.Content) != 0 {
				t.Fatalf("无正文响应发布了内容: %#v", response)
			}
		})
	}
}
