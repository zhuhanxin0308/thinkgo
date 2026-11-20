package framework

import (
	"fmt"
	"sort"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/v3/config"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
	"github.com/zhuhanxin0308/thinkgo/v3/route"
)

// applyRouteConfig 将 route.json 的全部字段应用到路由器。
func (app *App) applyRouteConfig() {
	if app == nil {
		return
	}
	app.applyRouteConfigTo(app.config, app.route)
}

func (app *App) applyRouteConfigTo(configuration *config.Config, router *route.Router) {
	if app == nil || configuration == nil || router == nil {
		return
	}
	routeConfig := configuration.GetMap("route")
	if len(routeConfig) == 0 {
		return
	}

	allowed := map[string]struct{}{
		"pathinfo_depr":         {},
		"url_lazy_route":        {},
		"url_route_must":        {},
		"url_case_sensitive":    {},
		"route_auto_group":      {},
		"route_rule_merge":      {},
		"route_complete_match":  {},
		"default_route_pattern": {},
		"default_controller":    {},
		"default_action":        {},
		"default_module":        {},
		"empty_controller":      {},
		"controller_layer":      {},
		"url_html_suffix":       {},
		"remove_slash":          {},
		"controller_suffix":     {},
		"action_suffix":         {},
		"action_bind_param":     {},
		"url_common_param":      {},
		"request_cache_key":     {},
		"request_cache_expire":  {},
		"request_cache_except":  {},
		"request_cache_tag":     {},
		"api_version":           {},
	}
	keys := make([]string, 0, len(routeConfig))
	for key := range routeConfig {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, exists := allowed[key]; !exists {
			app.recordStartupError(fmt.Errorf("route 配置包含未知字段 %q", key))
		}
	}

	caseSensitive, err := readRouteBool(routeConfig, "url_case_sensitive", false)
	if err != nil {
		app.recordStartupError(err)
	} else if err := router.SetCaseSensitive(caseSensitive); err != nil {
		app.recordStartupError(fmt.Errorf("设置 URL 大小写规则失败: %w", err))
	}
	completeMatch, err := readRouteBool(routeConfig, "route_complete_match", false)
	if err != nil {
		app.recordStartupError(err)
	} else if err := router.SetCompleteMatch(completeMatch); err != nil {
		app.recordStartupError(fmt.Errorf("设置路由完整匹配规则失败: %w", err))
	}
	removeSlash, err := readRouteBool(routeConfig, "remove_slash", false)
	if err != nil {
		app.recordStartupError(err)
	} else if err := router.SetRemoveSlash(removeSlash); err != nil {
		app.recordStartupError(fmt.Errorf("设置路由末尾斜杠规则失败: %w", err))
	}
	if _, err := readRouteBool(routeConfig, "controller_suffix", false); err != nil {
		app.recordStartupError(err)
	}
	if raw, exists := routeConfig["action_suffix"]; exists {
		if _, ok := raw.(string); !ok {
			app.recordStartupError(fmt.Errorf("route.action_suffix 必须是字符串"))
		}
	}
	if raw, exists := routeConfig["action_bind_param"]; exists {
		mode, ok := raw.(string)
		if !ok {
			app.recordStartupError(fmt.Errorf("route.action_bind_param 必须是字符串"))
		} else {
			switch strings.ToLower(strings.TrimSpace(mode)) {
			case "route", "get", "param":
			default:
				app.recordStartupError(fmt.Errorf("route.action_bind_param 必须是 route、get 或 param"))
			}
		}
	}

	if raw, exists := routeConfig["url_html_suffix"]; exists {
		extension, ok := raw.(string)
		if !ok {
			app.recordStartupError(fmt.Errorf("route.url_html_suffix 必须是字符串"))
		} else if err := router.SetDefaultExtension(extension); err != nil {
			app.recordStartupError(fmt.Errorf("设置 URL 后缀失败: %w", err))
		}
	}
	if raw, exists := routeConfig["default_route_pattern"]; exists {
		pattern, ok := raw.(string)
		if !ok {
			app.recordStartupError(fmt.Errorf("route.default_route_pattern 必须是字符串"))
		} else if err := router.SetDefaultPattern(pattern); err != nil {
			app.recordStartupError(fmt.Errorf("设置默认路由变量规则失败: %w", err))
		}
	}
	if raw, exists := routeConfig["controller_layer"]; exists {
		layer, ok := raw.(string)
		if !ok {
			app.recordStartupError(fmt.Errorf("route.controller_layer 必须是字符串"))
		} else if err := router.SetControllerLayer(layer); err != nil {
			app.recordStartupError(fmt.Errorf("设置控制器层失败: %w", err))
		}
	}
	if raw, exists := routeConfig["default_controller"]; exists {
		controller, ok := raw.(string)
		if !ok || strings.TrimSpace(controller) == "" {
			app.recordStartupError(fmt.Errorf("route.default_controller 必须是非空字符串"))
		} else if err := router.SetDefaultController(controller); err != nil {
			app.recordStartupError(fmt.Errorf("设置默认路由控制器失败: %w", err))
		}
	}
	if raw, exists := routeConfig["default_action"]; exists {
		action, ok := raw.(string)
		if !ok || strings.TrimSpace(action) == "" {
			app.recordStartupError(fmt.Errorf("route.default_action 必须是非空字符串"))
		} else if err := router.SetDefaultAction(action); err != nil {
			app.recordStartupError(fmt.Errorf("设置默认路由动作失败: %w", err))
		}
	}
	// 缺省配置与 NewRouter 保持一致：未声明规则的请求不自动调用控制器。
	mustRoute, err := readRouteBool(routeConfig, "url_route_must", true)
	if err != nil {
		app.recordStartupError(err)
	} else if err := router.EnableAutoRoute(!mustRoute); err != nil {
		app.recordStartupError(fmt.Errorf("设置自动路由开关失败: %w", err))
	}
}

// applyMiddlewareConfig 将 middleware.json 的 alias 和 priority 接入全局管道。
// Go 中间件不能从 JSON 反射构造，因此 alias 的值定义为已注册别名，并支持别名链。
func (app *App) applyMiddlewareConfig() {
	if app == nil {
		return
	}
	app.applyMiddlewareConfigTo(app.config, app.middleware)
}

func (app *App) applyMiddlewareConfigTo(configuration *config.Config, pipeline *middleware.Pipeline) {
	if app == nil || configuration == nil || pipeline == nil {
		return
	}
	values := configuration.GetMap("middleware")
	if len(values) == 0 {
		return
	}
	for key := range values {
		if key != "alias" && key != "priority" {
			app.recordStartupError(fmt.Errorf("middleware 配置包含未知字段 %q", key))
		}
	}

	if rawAliases, exists := values["alias"]; exists {
		aliases, ok := rawAliases.(map[string]interface{})
		if !ok {
			app.recordStartupError(fmt.Errorf("middleware.alias 必须是对象"))
		} else {
			app.applyMiddlewareAliasesTo(pipeline, aliases)
		}
	}
	if rawPriority, exists := values["priority"]; exists {
		var priority []interface{}
		ok := true
		switch typed := rawPriority.(type) {
		case []interface{}:
			priority = typed
		case []string:
			priority = make([]interface{}, len(typed))
			for index, name := range typed {
				priority[index] = name
			}
		default:
			ok = false
		}
		if !ok {
			app.recordStartupError(fmt.Errorf("middleware.priority 必须是字符串数组"))
			return
		}
		names := make([]string, 0, len(priority))
		seen := make(map[string]struct{}, len(priority))
		for index, rawName := range priority {
			name, ok := rawName.(string)
			name = strings.TrimSpace(name)
			if !ok || name == "" {
				app.recordStartupError(fmt.Errorf("middleware.priority[%d] 必须是非空字符串", index))
				continue
			}
			if _, exists := seen[name]; exists {
				app.recordStartupError(fmt.Errorf("middleware.priority 包含重复别名 %q", name))
				continue
			}
			if pipeline.ResolveAlias(name) == nil {
				app.recordStartupError(fmt.Errorf("middleware.priority 引用了未注册别名 %q", name))
				continue
			}
			seen[name] = struct{}{}
			names = append(names, name)
		}
		pipeline.SetPriority(names)
	}
}

func (app *App) applyMiddlewareAliasesTo(pipeline *middleware.Pipeline, values map[string]interface{}) {
	if app == nil || pipeline == nil {
		return
	}
	pending := make(map[string]string, len(values))
	keys := make([]string, 0, len(values))
	rawKeys := make([]string, 0, len(values))
	for key := range values {
		rawKeys = append(rawKeys, key)
	}
	sort.Strings(rawKeys)
	originalNames := make(map[string]string, len(values))
	for _, original := range rawKeys {
		alias := strings.TrimSpace(original)
		if previous, exists := originalNames[alias]; exists {
			// 发布前拒绝歧义；相同目标也不允许多个原始键争用一个别名。
			app.recordStartupError(fmt.Errorf("middleware.alias 包含规范化后重复别名 %q（原始键 %q 与 %q）", alias, previous, original))
			return
		}
		originalNames[alias] = original
		rawTarget := values[original]
		target, ok := rawTarget.(string)
		if !ok {
			app.recordStartupError(fmt.Errorf("middleware.alias[%q] 必须是已注册别名字符串", alias))
			continue
		}
		target = strings.TrimSpace(target)
		if alias == "" || target == "" {
			app.recordStartupError(fmt.Errorf("middleware.alias 包含空别名或空目标"))
			continue
		}
		if strings.ContainsAny(alias, "\r\n\x00") || strings.ContainsAny(target, "\r\n\x00") {
			app.recordStartupError(fmt.Errorf("middleware.alias %q 包含控制字符", alias))
			continue
		}
		pending[alias] = target
		keys = append(keys, alias)
	}
	sort.Strings(keys)

	for len(pending) > 0 {
		progress := false
		for _, alias := range keys {
			target, exists := pending[alias]
			if !exists {
				continue
			}
			handler := pipeline.ResolveAlias(target)
			if handler == nil {
				continue
			}
			pipeline.Alias(alias, handler)
			delete(pending, alias)
			progress = true
		}
		if progress {
			continue
		}
		for _, alias := range keys {
			if target, exists := pending[alias]; exists {
				app.recordStartupError(fmt.Errorf("middleware.alias %q 的目标 %q 未注册或存在循环引用", alias, target))
			}
		}
		return
	}
}

// readRouteBool 读取路由布尔字段，兼容 ThinkPHP 常见的 true、false、1 和 0 写法。
func readRouteBool(values map[string]interface{}, name string, defaultValue bool) (bool, error) {
	raw, exists := values[name]
	if !exists {
		return defaultValue, nil
	}
	switch value := raw.(type) {
	case bool:
		return value, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "true", "1":
			return true, nil
		case "false", "0":
			return false, nil
		}
	}
	return defaultValue, fmt.Errorf("route.%s 必须是布尔值", name)
}
