package route

import (
	"fmt"
	"net/http"
	"strings"
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
}{
	{action: "index", method: http.MethodGet, pathSuffix: ""},
	{action: "create", method: http.MethodGet, pathSuffix: "/create"},
	{action: "save", method: http.MethodPost, pathSuffix: ""},
	{action: "read", method: http.MethodGet, pathSuffix: "/:id"},
	{action: "edit", method: http.MethodGet, pathSuffix: "/:id/edit"},
	{action: "update", method: http.MethodPut, pathSuffix: "/:id"},
	{action: "delete", method: http.MethodDelete, pathSuffix: "/:id"},
}

// Resource 注册完整的 RESTful 资源路由集合。
func (r *Router) Resource(path, controller string) (*ResourceRoute, error) {
	return r.rootGroup().Resource(path, controller)
}

// Resource 注册当前分组作用域下的 RESTful 资源路由集合。
func (g *Group) Resource(path, controller string) (*ResourceRoute, error) {
	if g == nil || g.router == nil {
		return nil, ErrInvalidRoute
	}
	normalizedController, ok := normalizeResourceController(controller)
	if !ok {
		return nil, fmt.Errorf("%w: 资源控制器 %q 非法", ErrInvalidRouteHandler, controller)
	}
	trimmedPath := strings.TrimSpace(path)
	if strings.Contains(trimmedPath, "//") {
		return nil, fmt.Errorf("%w: 路径 %q 非法", ErrInvalidRoute, path)
	}
	basePath := strings.TrimRight(trimmedPath, "/")
	definitions := make([]routeDefinition, 0, len(resourceDefinitions))
	for _, definition := range resourceDefinitions {
		definitions = append(definitions, g.newDefinition(
			definition.method,
			basePath+definition.pathSuffix,
			normalizedController+"/"+definition.action,
			nil,
		))
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

// Routes 返回当前资源路由集合中的全部已注册路由快照。
func (r *ResourceRoute) Routes() []*Route {
	if r == nil || r.router == nil {
		return nil
	}
	r.router.mu.RLock()
	defer r.router.mu.RUnlock()
	result := make([]*Route, 0, len(resourceDefinitions))
	for _, definition := range resourceDefinitions {
		if registered := r.routes[definition.action]; registered != nil && !registered.removed {
			result = append(result, registered)
		}
	}
	return result
}

func normalizeResourceController(controller string) (string, bool) {
	if controller == "" {
		return "", false
	}
	normalized := strings.ReplaceAll(controller, ".", "/")
	parts := strings.Split(normalized, "/")
	for _, part := range parts {
		if !isValidAutoRouteSegment(part) {
			return "", false
		}
	}
	return strings.Join(parts, "/"), true
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
	if r.router.frozen.Load() {
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
