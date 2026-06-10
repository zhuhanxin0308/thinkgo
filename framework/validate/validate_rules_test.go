package validate

import "testing"

func TestValidatorCommonRules(t *testing.T) {
	validator := NewValidator()
	validator.Rule = map[string]string{
		"password":            "confirm",
		"nickname":            "different:email",
		"mobile":              "mobile",
		"birthday":            "dateFormat:2006-01-02|before:2025-01-01",
		"next_login_date":     "dateFormat:2006-01-02|after:2024-01-01",
		"company":             "requireIf:role,staff",
		"attachment_name":     "requireWith:attachment_path",
		"citizen_id":          "idCard",
		"verification_mobile": "requireIf:send_sms,true|mobile",
	}

	validData := map[string]interface{}{
		"password":            "secret123",
		"password_confirm":    "secret123",
		"nickname":            "tester",
		"email":               "tester@example.com",
		"mobile":              "13812345678",
		"birthday":            "2024-06-01",
		"next_login_date":     "2024-12-01",
		"role":                "staff",
		"company":             "ThinkGo",
		"attachment_path":     "/tmp/file.txt",
		"attachment_name":     "file.txt",
		"citizen_id":          "11010519491231002X",
		"send_sms":            "true",
		"verification_mobile": "13912345678",
	}

	if !validator.Check(validData) {
		t.Fatalf("合法数据应通过验证，实际错误为 %#v", validator.GetErrors())
	}

	invalidConfirm := cloneValidateData(validData)
	invalidConfirm["password_confirm"] = "changed"
	if validator.Check(invalidConfirm) {
		t.Fatal("confirm 规则应拦截不一致的确认字段")
	}

	invalidRequireIf := cloneValidateData(validData)
	delete(invalidRequireIf, "company")
	if validator.Check(invalidRequireIf) {
		t.Fatal("requireIf 规则应要求在满足条件时字段必填")
	}

	invalidRequireWith := cloneValidateData(validData)
	delete(invalidRequireWith, "attachment_name")
	if validator.Check(invalidRequireWith) {
		t.Fatal("requireWith 规则应要求关联字段同时存在")
	}

	invalidBefore := cloneValidateData(validData)
	invalidBefore["birthday"] = "2025-02-01"
	if validator.Check(invalidBefore) {
		t.Fatal("before 规则应拦截晚于截止日期的值")
	}

	invalidMobile := cloneValidateData(validData)
	invalidMobile["mobile"] = "123"
	if validator.Check(invalidMobile) {
		t.Fatal("mobile 规则应拦截非法手机号")
	}

	invalidIDCard := cloneValidateData(validData)
	invalidIDCard["citizen_id"] = "110105194912310021"
	if validator.Check(invalidIDCard) {
		t.Fatal("idCard 规则应校验校验码")
	}
}

func cloneValidateData(source map[string]interface{}) map[string]interface{} {
	cloned := make(map[string]interface{}, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}
