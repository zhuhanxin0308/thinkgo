package context

// RequestValueKey 是请求值的类型化键。用 new 创建键，只有持有同一键的代码可以读取或覆盖值。
// 类型化请求值独立于 Set、GetData 和 Middleware 使用的字符串键。
type RequestValueKey[T any] struct{ _ byte }

type requestValue[T any] struct{ value T }

// Store 在当前请求中设置键对应的值。空请求或空键会被忽略。
func (key *RequestValueKey[T]) Store(request *Request, value T) {
	if request == nil || key == nil {
		return
	}
	request.dataMu.Lock()
	defer request.dataMu.Unlock()
	if request.keyedData == nil {
		request.keyedData = make(map[any]any)
	}
	request.keyedData[key] = requestValue[T]{value}
}

// Load 返回当前请求中的值及其是否存在。调用方负责其中可变引用的所有权。
func (key *RequestValueKey[T]) Load(request *Request) (T, bool) {
	if request == nil || key == nil {
		var zero T
		return zero, false
	}
	request.dataMu.RLock()
	defer request.dataMu.RUnlock()
	value, exists := request.keyedData[key].(requestValue[T])
	return value.value, exists
}
