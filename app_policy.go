package framework

import (
	"fmt"
	"net/netip"
	"strings"
)

// SecurityProfile 描述应用实际使用的认证与浏览器状态边界。
type SecurityProfile string

const (
	// SecurityProfileStatelessAPI 表示应用只使用无状态凭据，不启用 Cookie Session 与 CSRF。
	SecurityProfileStatelessAPI SecurityProfile = "stateless_api"
	// SecurityProfileBrowserCookie 表示应用使用浏览器 Cookie Session，并必须启用 CSRF 防护。
	SecurityProfileBrowserCookie SecurityProfile = "browser_cookie"
)

// DatabaseStartupPolicy 描述数据库不可用时应用的启动行为。
type DatabaseStartupPolicy string

const (
	// DatabaseStartupLazy 表示按 ThinkPHP 默认行为仅校验连接配置，首次使用时才建立连接。
	DatabaseStartupLazy DatabaseStartupPolicy = "lazy"
	// DatabaseStartupRequired 表示默认数据库不可用必须阻止应用启动。
	DatabaseStartupRequired DatabaseStartupPolicy = "required"
	// DatabaseStartupDegraded 表示允许应用启动，但 readiness 必须保持失败。
	DatabaseStartupDegraded DatabaseStartupPolicy = "degraded"
	// DatabaseStartupDisabled 表示应用不装配数据库连接与数据库 readiness。
	DatabaseStartupDisabled DatabaseStartupPolicy = "disabled"
)

// OperationalAccess 描述运维端点是否由框架进行来源网络限制。
type OperationalAccess string

const (
	// OperationalAccessRestricted 表示只允许显式 CIDR 白名单访问运维端点。
	OperationalAccessRestricted OperationalAccess = "restricted"
	// OperationalAccessPublic 表示显式公开运维端点，部署审计会产生警告。
	OperationalAccessPublic OperationalAccess = "public"
)

// applyApplicationPolicies 解析跨安全、依赖和运维边界的显式应用策略。
func (app *App) applyApplicationPolicies() {
	if app == nil || app.config == nil {
		return
	}
	app.securityProfile = app.defaultSecurityProfile()
	app.databaseStartupPolicy = DatabaseStartupLazy
	app.operationalAccess = OperationalAccessRestricted
	app.operationalAllowedPrefixes = nil

	if app.config.Has("app.security_profile") {
		profile, err := readSecurityProfile(app.config.Get("app.security_profile"))
		if err != nil {
			app.recordStartupError(fmt.Errorf("app.security_profile 配置无效: %w", err))
		} else {
			app.securityProfile = profile
		}
	}
	if app.config.Has("app.database_startup_policy") {
		policy, err := readDatabaseStartupPolicy(app.config.Get("app.database_startup_policy"))
		if err != nil {
			app.recordStartupError(fmt.Errorf("app.database_startup_policy 配置无效: %w", err))
		} else {
			app.databaseStartupPolicy = policy
		}
	}
	if app.config.Has("app.operational_routes_access") {
		access, err := readOperationalAccess(app.config.Get("app.operational_routes_access"))
		if err != nil {
			app.recordStartupError(fmt.Errorf("app.operational_routes_access 配置无效: %w", err))
		} else {
			app.operationalAccess = access
		}
	}

	prefixesConfigured := app.config.Has("app.operational_routes_allowed_cidrs")
	prefixes, err := readOperationalPrefixes(app.config.Get("app.operational_routes_allowed_cidrs"))
	if err != nil {
		app.recordStartupError(fmt.Errorf("app.operational_routes_allowed_cidrs 配置无效: %w", err))
		return
	}
	app.operationalAllowedPrefixes = prefixes
	if app.operationalAccess == OperationalAccessPublic && len(prefixes) > 0 {
		app.recordStartupError(fmt.Errorf("app.operational_routes_allowed_cidrs 在 public 访问策略下必须为空"))
	}
	if app.operationalRoutesEnabled && app.operationalAccess == OperationalAccessRestricted && len(prefixes) == 0 {
		if prefixesConfigured {
			app.recordStartupError(fmt.Errorf("启用运维端点且访问策略为 restricted 时 app.operational_routes_allowed_cidrs 不能为空"))
			return
		}
		app.operationalAllowedPrefixes = defaultOperationalPrefixes()
	}
}

// defaultOperationalPrefixes 为旧配置提供安全兼容默认值，仅允许本机访问运维端点。
func defaultOperationalPrefixes() []netip.Prefix {
	return []netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("::1/128"),
	}
}

func (app *App) defaultSecurityProfile() SecurityProfile {
	if app != nil && (app.sessionEnabled || app.csrfEnabled) {
		return SecurityProfileBrowserCookie
	}
	return SecurityProfileStatelessAPI
}

func readSecurityProfile(raw interface{}) (SecurityProfile, error) {
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("必须是字符串")
	}
	switch SecurityProfile(strings.ToLower(strings.TrimSpace(value))) {
	case SecurityProfileStatelessAPI:
		return SecurityProfileStatelessAPI, nil
	case SecurityProfileBrowserCookie:
		return SecurityProfileBrowserCookie, nil
	default:
		return "", fmt.Errorf("必须是 %q 或 %q", SecurityProfileStatelessAPI, SecurityProfileBrowserCookie)
	}
}

func readDatabaseStartupPolicy(raw interface{}) (DatabaseStartupPolicy, error) {
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("必须是字符串")
	}
	switch DatabaseStartupPolicy(strings.ToLower(strings.TrimSpace(value))) {
	case DatabaseStartupLazy:
		return DatabaseStartupLazy, nil
	case DatabaseStartupRequired:
		return DatabaseStartupRequired, nil
	case DatabaseStartupDegraded:
		return DatabaseStartupDegraded, nil
	case DatabaseStartupDisabled:
		return DatabaseStartupDisabled, nil
	default:
		return "", fmt.Errorf("必须是 %q、%q、%q 或 %q", DatabaseStartupLazy, DatabaseStartupRequired, DatabaseStartupDegraded, DatabaseStartupDisabled)
	}
}

func readOperationalAccess(raw interface{}) (OperationalAccess, error) {
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("必须是字符串")
	}
	switch OperationalAccess(strings.ToLower(strings.TrimSpace(value))) {
	case OperationalAccessRestricted:
		return OperationalAccessRestricted, nil
	case OperationalAccessPublic:
		return OperationalAccessPublic, nil
	default:
		return "", fmt.Errorf("必须是 %q 或 %q", OperationalAccessRestricted, OperationalAccessPublic)
	}
}

func readOperationalPrefixes(raw interface{}) ([]netip.Prefix, error) {
	if raw == nil {
		return nil, nil
	}
	var values []string
	switch typed := raw.(type) {
	case []string:
		values = append(values, typed...)
	case []interface{}:
		values = make([]string, 0, len(typed))
		for _, item := range typed {
			value, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("列表元素必须是字符串")
			}
			values = append(values, value)
		}
	default:
		return nil, fmt.Errorf("必须是 CIDR 字符串列表")
	}

	prefixes := make([]netip.Prefix, 0, len(values))
	seen := make(map[netip.Prefix]struct{}, len(values))
	for _, value := range values {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
		if err != nil || prefix.Bits() == 0 {
			return nil, fmt.Errorf("CIDR %q 非法或覆盖全部地址", value)
		}
		prefix = prefix.Masked()
		if _, exists := seen[prefix]; exists {
			return nil, fmt.Errorf("CIDR %q 重复", value)
		}
		seen[prefix] = struct{}{}
		prefixes = append(prefixes, prefix)
	}
	return prefixes, nil
}
