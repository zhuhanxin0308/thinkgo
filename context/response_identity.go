package context

// ResponseIdentity 是 Response 在框架生命周期内保持稳定的不可变身份。
// Response 值被复制时会继续共享同一身份，HTTP 宿主因此不依赖对象地址追踪请求清理状态。
type ResponseIdentity struct {
	_ byte
}

// Identity 返回当前响应的稳定身份。
// 框架构造器会提前创建身份；零值 Response 则在首次进入宿主生命周期时补建。
func (r *Response) Identity() *ResponseIdentity {
	if r == nil {
		return nil
	}
	if r.identity == nil {
		r.identity = &ResponseIdentity{}
	}
	return r.identity
}
