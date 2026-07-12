package controller

import "thinkgo/framework"

// User 用户控制器，演示控制器注册和基础动作分发。
type User struct {
	framework.Controller
}

func init() {
	// 注册控制器类型，HTTP 内核会在每次请求时创建新实例，避免并发状态串扰。
	framework.MustRegisterController("User", &User{})
}

// Index 返回用户列表入口的示例响应。
func (c *User) Index() string {
	return "User List"
}
