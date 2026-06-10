package validate

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
	v.Rule = map[string]string{
		"hash":           "required",
		"file_extension": "required",
		"mime_type":      "required",
		"id":             "required|integer",
	}
	v.Message = map[string]string{
		"hash.required":           "validate.file_hash_required",
		"file_extension.required": "validate.file_ext_required",
		"mime_type.required":      "validate.file_mime_required",
		"id.required":             "validate.file_id_required",
		"id.integer":              "validate.file_id_integer",
	}
	v.Scene = map[string][]string{
		"check":          {"hash"},
		"instant_upload": {"hash", "file_extension", "mime_type"},
		"delete":         {"id"},
	}
	return v
}
