package validate

// UserFileValidate 是 index 应用的文件验证器。

import (
	"thinkgo/framework/validate"
)

// UserFileValidate 用户文件验证器
type UserFileValidate struct {
	validate.Validator
}

// NewUserFileValidate 创建用户文件验证器
func NewUserFileValidate() *UserFileValidate {
	v := &UserFileValidate{}
	v.SetRules(map[string]string{
		"hash":           "required",
		"file_extension": "required",
		"mime_type":      "required",
		"id":             "required|integer",
	})
	v.SetMessages(map[string]string{
		"hash.required":           "validate.file_hash_required",
		"file_extension.required": "validate.file_ext_required",
		"mime_type.required":      "validate.file_mime_required",
		"id.required":             "validate.file_id_required",
		"id.integer":              "validate.file_id_integer",
	})
	v.SetScenes(map[string][]string{
		"check":          {"hash"},
		"instant_upload": {"hash", "file_extension", "mime_type"},
		"delete":         {"id"},
	})
	return v
}
