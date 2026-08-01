package model

import (
	"errors"
	"testing"

	"thinkgo/framework/db"
)

// TestNewUserRejectsDatabaseFreeOperations 验证示例模型仍可构造，
// 但在没有数据库时不会伪装成可执行模型。
func TestNewUserRejectsDatabaseFreeOperations(t *testing.T) {
	user := NewUser(nil)
	if user == nil || user.Model == nil {
		t.Fatal("用户模型必须返回完整模型对象")
	}
	if _, err := user.Find(); !errors.Is(err, db.ErrInvalidModel) {
		t.Fatalf("无数据库模型操作应返回 ErrInvalidModel: %v", err)
	}
}
