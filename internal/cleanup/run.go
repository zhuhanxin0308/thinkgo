// Package cleanup 为同一协程中的延后清理步骤记录真实完成状态。
package cleanup

import (
	"errors"
	"fmt"
)

// ErrAborted 表示清理回调通过 Goexit 退出，未能正常返回结果。
var ErrAborted = errors.New("清理回调异常退出")

// Run 聚合回调错误并识别 Goexit，普通 panic 仍按原值传播。
// 调用方应通过 defer 登记需要继续执行的步骤，并在这些步骤之后发布完成；
// 本函数不会吞掉 Goexit、重试回调或另外创建协程。
func Run(result *error, stage string, callback func() error) {
	callbackErr := Call(callback, func(panicked bool) {
		if !panicked {
			*result = errors.Join(*result, fmt.Errorf("%s: %w", stage, ErrAborted))
		}
	})
	if callbackErr != nil {
		*result = errors.Join(*result, callbackErr)
	}
}
