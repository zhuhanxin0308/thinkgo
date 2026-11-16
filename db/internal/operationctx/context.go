// Package operationctx 区分框架默认截止时间和调用方显式期限。
package operationctx

import (
	"context"
	"time"
)

type frameworkDefault struct {
	context.Context
	caller context.Context
}

// WithFrameworkDefault 为没有截止时间的查询设置通用预算。
func WithFrameworkDefault(caller context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if _, exists := caller.Deadline(); exists {
		return caller, func() {}
	}
	bounded, cancel := context.WithTimeout(caller, timeout)
	return &frameworkDefault{Context: bounded, caller: caller}, cancel
}

// WithDriverDefault 使用连接配置的预算，保留调用方原本的截止时间和取消信号。
func WithDriverDefault(caller context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if fallback, ok := caller.(*frameworkDefault); ok {
		caller = fallback.caller
	}
	if _, exists := caller.Deadline(); exists {
		return caller, func() {}
	}
	return context.WithTimeout(caller, timeout)
}
