package cleanup

// Catch 把回调的 panic 转换成结果；Goexit 继续展开原协程的 defer，不执行 onPanic。
// 常规异常在原始栈上处理，处理器自身的异常则继续向调用方传播。
func Catch[T any](callback func() T, onPanic func(any) T) (result T) {
	completed := false
	func() {
		defer func() {
			if !completed {
				if value := recover(); value != nil {
					result = onPanic(value)
					completed = true
				}
			}
		}()
		result = callback()
		completed = true
	}()
	// 旧模式的 nil panic 被内层 recover 后会返回到这里；Goexit 不会返回。
	if !completed {
		result = onPanic(nil)
	}
	return result
}

// Call 执行回调并返回原错误。panic 或 Goexit 时先在同一协程调用 onExit，再继续原退出过程。
// onExit 的参数区分 panic（包括旧模式的 nil panic）和 Goexit。
func Call(callback func() error, onExit func(panicked bool)) (err error) {
	returned, panicked := false, false
	defer func() {
		if !returned {
			onExit(panicked)
		}
	}()
	err = Catch(callback, func(value any) error {
		panicked = true
		panic(value)
	})
	returned = true
	return err
}
