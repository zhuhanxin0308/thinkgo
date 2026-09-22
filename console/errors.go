package console

import "errors"

var (
	// ErrInvalidCommand 表示命令为空、签名非法或声明存在冲突。
	ErrInvalidCommand = errors.New("命令定义非法")
	// ErrDuplicateCommand 表示同名命令已注册，禁止静默覆盖。
	ErrDuplicateCommand = errors.New("命令重复注册")
	// ErrCommandNotFound 表示调用了未注册的命令。
	ErrCommandNotFound = errors.New("命令不存在")
	// ErrCommandNotImplemented 表示具体命令未覆盖基础 Execute 实现。
	ErrCommandNotImplemented = errors.New("命令未实现")
	// ErrInvalidInput 表示命令行参数或选项不符合命令声明。
	ErrInvalidInput = errors.New("命令行输入非法")
	// ErrInvalidOutput 表示命令输出器或其目标写入器不可用。
	ErrInvalidOutput = errors.New("命令输出不可用")
	// ErrInvalidConfig 表示控制台配置无法应用。
	ErrInvalidConfig = errors.New("控制台配置非法")
)
