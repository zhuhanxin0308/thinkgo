package event

// HasApplicationListener 判断事件是否匹配至少一个应用级监听器。
// 与 HasListener 相同，名称先解析注册别名，再合并精确及通配匹配；
// 本方法不调用监听器，也不表示项目级 DispatchProjectContext 会执行它。
// 返回值是并发安全的瞬时快照，注册或移除后可改变；nil 接收者及非法名称返回 false。
func (d *Dispatcher) HasApplicationListener(eventName string) bool {
	if d == nil || validateEventName(eventName, false) != nil {
		return false
	}
	eventName = d.boundEventName(eventName)
	for _, entry := range d.resolveListeners(eventName) {
		if entry.application {
			return true
		}
	}
	return false
}
