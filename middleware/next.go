package middleware

import "github.com/zhuhanxin0308/thinkgo/v3/context"

// Next 是调用链的下游函数别名，同时兼容旧版本生成的中间件源码。
// 类型别名保持现有 Handler 的函数签名，不要求调用方进行类型转换。
type Next = func(*context.Request) *context.Response
