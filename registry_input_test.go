package framework

import (
	"errors"
	"testing"

	requestbinding "github.com/zhuhanxin0308/thinkgo/v3/binding"
)

type invalidControllerInput struct {
	requestbinding.Input
	Page int `query:"page" default:"bad"`
}
type validControllerInput struct {
	requestbinding.Input
	Page int `query:"page" default:"1"`
}
type invalidInputController struct{}
type validInputController struct{}

func (*invalidInputController) List(invalidControllerInput) string { return "unreachable" }
func (*validInputController) List(validControllerInput) string     { return "valid" }

// TestControllerInputDefinitionFailsAtRegistration 验证非法请求定义在启动装配前失败且不占用控制器名称。
func TestControllerInputDefinitionFailsAtRegistration(t *testing.T) {
	app := NewConsoleAppUninitialized(t.TempDir())
	t.Cleanup(func() {
		if err := app.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := app.RegisterController("Contract", &invalidInputController{}); !errors.Is(err, ErrInvalidRegistration) || !errors.Is(err, requestbinding.ErrDefinition) {
		t.Fatalf("错误定义没有在注册期拒绝: %v", err)
	}
	if err := app.RegisterController("Contract", &validInputController{}); err != nil {
		t.Fatalf("失败注册占用名称: %v", err)
	}
}
