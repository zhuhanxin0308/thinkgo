package cleanup

import "context"

type terminalResultContext struct {
	context.Context
	result *error
}

// WithTerminalResult 供 HTTP 监督者在串行事件回调结束原协程时接收完整分发结果。
func WithTerminalResult(ctx context.Context, result *error) context.Context {
	return &terminalResultContext{Context: ctx, result: result}
}

// UnwrapTerminalResult 只接收直接交给终止分发的结果槽，恢复传给监听器的原始上下文。
func UnwrapTerminalResult(ctx context.Context) (context.Context, *error) {
	if terminal, ok := ctx.(*terminalResultContext); ok {
		return terminal.Context, terminal.result
	}
	return ctx, nil
}
