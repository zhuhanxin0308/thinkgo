package controller

import "testing"

// TestUserIndex 验证示例用户控制器的默认列表动作保持兼容响应。
func TestUserIndex(t *testing.T) {
	controller := &User{}
	if got := controller.Index(); got != "User List" {
		t.Fatalf("用户列表动作响应错误: got %q", got)
	}
}
