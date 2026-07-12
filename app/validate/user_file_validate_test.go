package validate

import (
	"testing"

	frameworkvalidate "thinkgo/framework/validate"
)

// TestUserFileValidateScenes 验证用户文件验证器的场景、规则和消息均使用新 API 生效。
func TestUserFileValidateScenes(t *testing.T) {
	validator := NewUserFileValidate()

	checkResult, err := validator.Validate(map[string]interface{}{}, frameworkvalidate.WithScene("check"))
	if err != nil || checkResult.FirstError() != "validate.file_hash_required" {
		t.Fatalf("秒传检查场景验证错误: result=%#v err=%v", checkResult, err)
	}

	deleteResult, err := validator.Validate(
		map[string]interface{}{"id": "invalid"},
		frameworkvalidate.WithScene("delete"),
	)
	violations := deleteResult.Violations()
	if err != nil || len(violations) != 1 || violations[0].Rule != "integer" {
		t.Fatalf("删除场景应校验整数 ID: result=%#v err=%v", deleteResult, err)
	}
}
