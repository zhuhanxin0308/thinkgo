package route

import (
	"fmt"
	"net/http"
	"strings"

	"thinkgo/framework/middleware"
)

// ResourceRoute 表示一组可原子裁剪的 RESTful 资源路由。
type ResourceRoute struct {
	router *Router
	routes map[string]*Route
}

var resourceDefinitions = []struct {
	action     string
	method     string
	pathSuffix string
	handler    string
}{
	{action: "index", method: http.MethodGet, pathSuffix: "", handler: "@Index"},
	{action: "create", method: http.MethodGet, pathSuffix: "/create", handler: "@Create"},
	{action: "save", method: http.MethodPost, pathSuffix: "", handler: "@Save"},
	{action: "read", method: http.MethodGet, pathSuffix: "/:id", handler: "@Read"},
	{action: "edit", method: http.MethodGet, pathSuffix: "/:id/edit", handler: "@Edit"},
	{action: "update", method: http.MethodPut, pathSuffix: "/:id", handler: "@Update"},
	{action: "delete", method: http.MethodDelete, pathSuffix: "/:id", handler: "@Delete"},
}

// Resource 注册完整的 RESTful 资源路由集合。
func (r *Router) Resource(path, controller string) (*ResourceRoute, error) {
	return r.rootGroup().Resource(path, controller)
}

// Resource 注册当前分组作用域下的 RESTful 资源路由集合。
func (g *Group) Resource(path, controller string) (*ResourceRoute, error) {
	if !isValidAutoControllerName(controller) {
		return nil, fmt.Errorf("%w: 资源控制器 %q 非法", ErrInvalidRouteHandler, controller)
	}
	definitions := make([]routeDefinition, 0, len(resourceDefinitions))
	for _, definition := range resourceDefinitions {
		definitions = append(definitions, routeDefinition{
			method:      definition.method,
			path:        joinRoutePath(g.prefix, path+definition.pathSuffix),
			handler:     controller + definition.handler,
			middlewares: append([]middleware.Handler(nil), g.middlewares...),
			domain:      g.domain,
		})
	}
	routes, err := g.router.registerDefinitions(definitions)
	if err != nil {
		return nil, err
	}
	resource := &ResourceRoute{router: g.router, routes: make(map[string]*Route, len(routes))}
	for index, definition := range resourceDefinitions {
		resource.routes[definition.action] = routes[index]
	}
	return resource, nil
}

// Only 保留指定的资源动作。
func (r *ResourceRoute) Only(actions ...string) error {
	if len(actions) == 0 {
		return fmt.Errorf("%w: Only 至少需要一个动作", ErrInvalidResourceAction)
	}
	selected, err := validateResourceActions(actions)
	if err != nil {
		return err
	}
	return r.filter(func(action string) bool { return selected[action] })
}

// Except 删除指定的资源动作。
func (r *ResourceRoute) Except(actions ...string) error {
	selected, err := validateResourceActions(actions)
	if err != nil {
		return err
	}
	return r.filter(func(action string) bool { return !selected[action] })
}

func (r *ResourceRoute) filter(keep func(string) bool) error {
	if r == nil || r.router == nil {
		return ErrInvalidRoute
	}
	r.router.mu.Lock()
	defer r.router.mu.Unlock()
	if r.router.frozen {
		return ErrRouterFrozen
	}
	for action, registered := range r.routes {
		if keep(action) {
			continue
		}
		r.router.removeRouteLocked(registered)
		delete(r.routes, action)
	}
	return nil
}

func validateResourceActions(actions []string) (map[string]bool, error) {
	valid := make(map[string]bool, len(resourceDefinitions))
	for _, definition := range resourceDefinitions {
		valid[definition.action] = true
	}
	selected := make(map[string]bool, len(actions))
	for _, action := range actions {
		action = strings.ToLower(strings.TrimSpace(action))
		if !valid[action] {
			return nil, fmt.Errorf("%w: %q", ErrInvalidResourceAction, action)
		}
		selected[action] = true
	}
	return selected, nil
}
