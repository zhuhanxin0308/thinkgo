package model

// User 模型属于 index 应用。

import (
	"thinkgo/framework/db"
)

// User 用户主模型
// 表名显式指定为: users
type User struct {
	*db.Model
}

// NewUser 创建用户模型实例
func NewUser(database *db.DB) *User {
	m := &User{}
	m.Model = db.NewModel(database, "users")
	return m
}

// 用户状态常量
const (
	UserStatusNormal   = 0 // 正常
	UserStatusDisabled = 1 // 禁用
	UserStatusLocked   = 2 // 锁定
)
