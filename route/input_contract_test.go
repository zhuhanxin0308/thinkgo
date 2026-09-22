package route

import (
	"errors"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/binding"
)

type invalidRouteInput struct {
	binding.Input
	Name string `query:"name" validate:"unknownRule"`
}
type validRouteInput struct {
	binding.Input
	Name string `query:"name"`
}

// TestRouteInputDefinitionFailsAtRegistration 验证字段规则在路由注册期编译，失败后路由表仍可继续配置。
func TestRouteInputDefinitionFailsAtRegistration(t *testing.T) {
	router := NewRouter()
	if _, err := router.Get("/input", func(invalidRouteInput) string { return "unreachable" }); !errors.Is(err, binding.ErrDefinition) {
		t.Fatalf("错误定义没有在注册期拒绝: %v", err)
	}
	if _, err := router.Get("/input", func(validRouteInput) string { return "valid" }); err != nil {
		t.Fatalf("错误注册污染路由表: %v", err)
	}
}
