package controller

import "thinkgo/framework"

// User 用户控制器，演示控制器注册和基础动作分发。
type User struct {
	framework.Controller
}

// Index 返回用户列表入口的示例响应。
func (c *User) Index() string {
	return "User List"
}
