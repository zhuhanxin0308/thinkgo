package context

// Put 获取 PUT 请求体参数。ThinkPHP 的 PUT、DELETE、PATCH 共用同一输入数据集。
func (r *Request) Put(key string, defaults ...string) string {
	return r.Post(key, defaults...)
}

// Delete 获取 DELETE 请求体参数。
func (r *Request) Delete(key string, defaults ...string) string {
	return r.Put(key, defaults...)
}

// Patch 获取 PATCH 请求体参数。
func (r *Request) Patch(key string, defaults ...string) string {
	return r.Put(key, defaults...)
}

// Request 获取合并后的请求参数，优先级与 Param 一致。
func (r *Request) Request(key string, defaults ...string) string {
	return r.Param(key, defaults...)
}

// GetContent 获取当前请求的原始内容；读取失败时返回空字符串，错误可由 BodyReadError 获取。
func (r *Request) GetContent() string {
	body, err := r.Body()
	if err != nil {
		return ""
	}
	return string(body)
}

// GetInput 获取当前请求的原始输入。
func (r *Request) GetInput() string {
	return r.GetContent()
}
