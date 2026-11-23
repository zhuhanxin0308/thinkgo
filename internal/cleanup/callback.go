package cleanup

// Call 执行回调并返回原错误。panic 或 Goexit 时先在同一协程调用 onExit，再继续原退出过程。
// onExit 的参数区分 panic（包括旧模式的 nil panic）和 Goexit。
func Call(callback func() error, onExit func(panicked bool)) (err error) {
	returned, panicked := false, false
	defer func() {
		if !returned {
			onExit(panicked)
		}
	}()
	var panicValue interface{}
	func() {
		defer func() {
			if !returned {
				panicValue = recover()
			}
		}()
		err = callback()
		returned = true
	}()
	// 旧模式的 nil panic 也会使内层 recover 后返回。Goexit 只展开 defer，无法抵达这里。
	if !returned {
		panicked = true
		panic(panicValue)
	}
	return err
}
