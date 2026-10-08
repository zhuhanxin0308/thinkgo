package db

import "errors"

// ErrDatabaseOperationFailed 是展示边界可使用的固定错误，不携带驱动细节。
// 它不描述写入是否完成、事务是否提交或操作能否重试；这些判断必须使用原始错误。
var ErrDatabaseOperationFailed = errors.New("数据库操作失败")

// SanitizeError 将非 nil 错误转换为固定的 ErrDatabaseOperationFailed；nil 保持 nil。
// 这是供 HTTP/其他展示边界显式调用的有损转换，不改变数据库方法的错误契约。
//
// 返回值不包装、持有或解包原始错误，不暴露 SQL、SQLSTATE、约束名、参数或凭据。
// 本方法不调用传入错误的 Error、Unwrap、Is、As 或格式化方法；类型化 nil error
// 仍按非 nil 接口处理。不要用转换后的错误进行事务、取消、部分写入或重试判断，
// 也不要在转换后又把原始错误拼接到响应中。需要这些判断时，先保留并处理原始错误。
func SanitizeError(err error) error {
	if err == nil {
		return nil
	}
	return ErrDatabaseOperationFailed
}
