package framework

import (
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
	"github.com/zhuhanxin0308/thinkgo/v3/route"
)

// AddWithCommit 为接口契约提供显式错误入口，并保留门面当前的分组作用域。
// commit 在底层注册锁内执行，不能重新调用该路由器，失败时须保持关联状态不变。
func (r *Route) AddWithCommit(method, path string, handler route.HandlerFunc, commit func(route.RouteInfo) error, handlers ...middleware.Handler) (*route.Route, error) {
	if r == nil || r.router == nil {
		return nil, route.ErrInvalidRoute
	}
	var (
		registered *route.Route
		err        error
	)
	if group := r.currentScope(); group != nil {
		registered, err = group.AddWithCommit(method, path, handler, commit, handlers...)
	} else {
		registered, err = r.router.AddWithCommit(method, path, handler, commit, handlers...)
	}
	if err == nil && registered != nil {
		r.recordRoutes(registered)
	}
	return registered, err
}
