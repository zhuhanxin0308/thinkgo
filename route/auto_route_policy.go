package route

// AutoRouteEnabled 返回当前路由器的自动调度策略，供部署审计读取实际配置。
// 读取不会冻结路由，也不会改变 ThinkPHP 默认自动调度行为。
func (r *Router) AutoRouteEnabled() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.autoRoute
}
