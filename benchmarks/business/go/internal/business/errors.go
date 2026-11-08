package business

import "errors"

// PublicError 区分可展示的业务失败与必须保留在服务日志中的内部错误。
type PublicError struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (err *PublicError) Error() string { return err.Code + ": " + err.Message }

func InvalidInput(message string) error {
	return &PublicError{Status: 422, Code: "validation_error", Message: message}
}

func unauthorized() error {
	return &PublicError{Status: 401, Code: "unauthorized", Message: "身份凭证无效或已过期"}
}

func notFound() error {
	return &PublicError{Status: 404, Code: "not_found", Message: "记录不存在"}
}

func forbidden() error {
	return &PublicError{Status: 403, Code: "forbidden", Message: "没有操作该记录的权限"}
}

func conflict(message string) error {
	return &PublicError{Status: 409, Code: "conflict", Message: message}
}

func ErrorResult(err error) (int, any) {
	var public *PublicError
	if !errors.As(err, &public) {
		public = &PublicError{Status: 500, Code: "internal_error", Message: "服务暂时不可用"}
	}
	return public.Status, struct {
		Error *PublicError `json:"error"`
	}{Error: public}
}
