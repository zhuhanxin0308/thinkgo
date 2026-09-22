package http

import (
	"net/http"

	"github.com/zhuhanxin0308/thinkgo/framework/exception"
)

// actionParameterError 仅暴露字段与错误类别，避免解析器将原始输入带入响应。
func actionParameterError(field, rule, message string) error {
	return exception.NewHttpException(http.StatusBadRequest, message).WithData(map[string]interface{}{
		"field": field,
		"rule":  rule,
	})
}
