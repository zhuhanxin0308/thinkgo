package framework

import (
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/cookie"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
	"github.com/zhuhanxin0308/thinkgo/v3/session"
	sessionDriver "github.com/zhuhanxin0308/thinkgo/v3/session/driver"
)

const (
	securityEnvironmentProduction        = "production"
	securityWarningCookieSecret   uint32 = 1 << iota
	securityWarningCSRFSecret
	securityWarningAllowedHosts
	securityWarningCSRFDisabled
	securityWarningCookieTransport
	securityWarningCSRFTransport
	securityWarningProfile
	securityWarningProfileMismatch
	securityWarningSessionDisabled
)

// createAppCookie 创建经过严格配置校验的全局 Cookie 工厂。
func createAppCookie(raw map[string]interface{}) (*cookie.Cookie, error) {
	factory, err := cookie.NewCookie(raw)
	if err != nil {
		return nil, fmt.Errorf("解析 Cookie 配置失败: %w", err)
	}
	return factory, nil
}

// fallbackAppCookie 只用于承载启动错误后的非空对象，应用不会在 StartupError 下进入服务状态。
func fallbackAppCookie() *cookie.Cookie {
	factory, _ := cookie.NewCookieWithConfig(cookie.DefaultConfig())
	return factory
}

// createAppSession 先严格解析配置，再创建与配置类型一致的驱动和管理器。
func createAppSession(app *App, raw map[string]interface{}, cookieFactory *cookie.Cookie) (*session.Session, error) {
	if app == nil || cookieFactory == nil {
		return nil, session.ErrInvalidSessionDependency
	}
	config, err := session.ParseConfig(raw)
	if err != nil {
		return nil, err
	}
	var backend session.Driver
	switch config.DriverType {
	case "cache":
		if app.cache == nil {
			return nil, fmt.Errorf("%w: Session cache 驱动依赖缓存服务", session.ErrInvalidSessionDependency)
		}
		store := app.cache
		if config.Store != "" {
			store, err = app.cache.Store(config.Store)
			if err != nil {
				return nil, fmt.Errorf("解析 Session 缓存 store %q 失败: %w", config.Store, err)
			}
		}
		backend = newCacheSessionDriver(
			store,
			time.Duration(config.Expire)*time.Second,
			cacheSessionScope(app, config.Name, config.Store, config.Prefix),
		)
	case "memory":
		backend, err = sessionDriver.NewMemoryWithMaxEntries(config.MaxEntries)
		if err != nil {
			return nil, err
		}
	case "file":
		config.StoragePath, err = app.resolveStoragePath(config.StoragePath)
		if err != nil {
			return nil, fmt.Errorf("解析 Session 存储路径失败: %w", err)
		}
		backend, err = sessionDriver.NewFile(config.StoragePath)
		if err != nil {
			return nil, err
		}
	case "redis":
		redisConfig, ok := raw["redis"].(map[string]interface{})
		if !ok || len(redisConfig) == 0 {
			return nil, fmt.Errorf("%w: Redis 配置不能为空", session.ErrInvalidSessionConfig)
		}
		backend, err = sessionDriver.NewRedis(redisConfig)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("%w: 未知驱动 %q", session.ErrInvalidSessionConfig, config.DriverType)
	}
	return session.NewSessionWithConfig(config, backend, cookieFactory)
}

// fallbackAppSession 构造内存型安全退化服务，但保留启动错误以阻止应用对外服务。
func fallbackAppSession(cookieFactory *cookie.Cookie) *session.Session {
	config := session.DefaultConfig()
	config.DriverType = "memory"
	backend, err := sessionDriver.NewMemoryWithMaxEntries(config.MaxEntries)
	if err != nil {
		return nil
	}
	manager, _ := session.NewSessionWithConfig(config, backend, cookieFactory)
	return manager
}

// createAppCSRF 使用独立配置；未配置密钥时复用 Cookie 密钥，否则生成进程级随机密钥。
func createAppCSRF(raw map[string]interface{}, cookieFactory *cookie.Cookie) (middleware.Handler, error) {
	config, err := middleware.ParseCSRFConfig(raw)
	if err != nil {
		return nil, err
	}
	if config.Secret == "" && cookieFactory != nil {
		config.Secret = cookieFactory.GetConfig().Secret
	}
	return middleware.CsrfWithConfig(config)
}

// unavailableCSRFHandler 在启动期构造失败后保持别名非空，并拒绝所有请求。
func unavailableCSRFHandler(req *context.Request, next func(*context.Request) *context.Response) *context.Response {
	return context.NewResponse().Abort(http.StatusInternalServerError, map[string]interface{}{
		"message": "CSRF 服务不可用",
	})
}

// warnProductionSecurity 在生产环境执行一次性分类安全检查，不安全配置会阻止服务启动。
func (app *App) warnProductionSecurity() {
	if app == nil || !strings.EqualFold(app.securityWarningEnvironment(), securityEnvironmentProduction) {
		return
	}
	profile := app.securityProfile
	if app.config != nil && app.config.Has("app.security_profile") {
		parsed, err := readSecurityProfile(app.config.Get("app.security_profile"))
		if err != nil {
			app.emitSecurityWarning(securityWarningProfile, "生产环境安全警告：security_profile 配置无效")
			return
		}
		profile = parsed
	} else {
		app.emitSecurityWarning(securityWarningProfile, "生产环境安全警告：必须显式配置 security_profile")
	}
	if profile == "" {
		profile = app.defaultSecurityProfile()
	}

	if !app.hasAllowedHosts() {
		app.emitSecurityWarning(securityWarningAllowedHosts, "生产环境安全警告：allowed_hosts 未配置具体白名单或使用了 *")
	}
	sessionEnabled := app.sessionEnabled
	csrfEnabled := app.csrfEnabled
	if app.config != nil {
		sessionEnabled = app.config.GetBool("app.session_enable", sessionEnabled)
		csrfEnabled = app.config.GetBool("app.csrf_enable", csrfEnabled)
	}
	if profile == SecurityProfileStatelessAPI {
		if sessionEnabled || csrfEnabled {
			app.emitSecurityWarning(securityWarningProfileMismatch, "生产环境安全警告：stateless_api 必须关闭 Session 与 CSRF Cookie")
		}
		return
	}
	if !sessionEnabled {
		app.emitSecurityWarning(securityWarningSessionDisabled, "生产环境安全警告：browser_cookie 必须启用 Session")
	}

	cookieSecret := ""
	if app.cookie != nil {
		cookieSecret = strings.TrimSpace(app.cookie.GetConfig().Secret)
	}
	if cookieSecret == "" {
		app.emitSecurityWarning(securityWarningCookieSecret, "生产环境安全警告：Cookie/Session 密钥为空")
	}
	if app.cookie == nil || !app.cookie.GetConfig().Secure {
		app.emitSecurityWarning(securityWarningCookieTransport, "生产环境安全警告：Cookie/Session Cookie 未启用 Secure")
	}

	csrfSecret := ""
	if app.config != nil {
		if configured, ok := app.config.GetMap("csrf")["secret"].(string); ok {
			csrfSecret = strings.TrimSpace(configured)
		}
	}
	if csrfSecret == "" {
		csrfSecret = cookieSecret
	}
	if csrfSecret == "" {
		app.emitSecurityWarning(securityWarningCSRFSecret, "生产环境安全警告：CSRF 密钥为空")
	}

	if !csrfEnabled {
		app.emitSecurityWarning(securityWarningCSRFDisabled, "生产环境安全警告：CSRF 未启用")
	} else if !app.config.GetBool("csrf.secure", false) {
		app.emitSecurityWarning(securityWarningCSRFTransport, "生产环境安全警告：CSRF Cookie 未启用 Secure")
	}
}

func (app *App) securityWarningEnvironment() string {
	if app == nil {
		return ""
	}
	return normalizeSecurityEnvironment(app.Environment())
}

func normalizeSecurityEnvironment(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "prod", "production", "release":
		return securityEnvironmentProduction
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func (app *App) hasAllowedHosts() bool {
	if app == nil {
		return false
	}
	if app.config == nil {
		return false
	}
	values := app.config.Get("app.server.allowed_hosts")
	switch typed := values.(type) {
	case string:
		return hasConcreteAllowedHosts(strings.Split(typed, ","))
	case []string:
		return hasConcreteAllowedHosts(typed)
	case []interface{}:
		values := make([]string, 0, len(typed))
		for _, value := range typed {
			if text, ok := value.(string); ok {
				values = append(values, text)
			}
		}
		return hasConcreteAllowedHosts(values)
	}
	return false
}

// hasConcreteAllowedHosts 判断生产 Host 配置是否包含至少一个具体主机且没有全匹配通配符。
func hasConcreteAllowedHosts(values []string) bool {
	configured := false
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		configured = true
		if value == "*" {
			return false
		}
	}
	return configured
}

func (app *App) emitSecurityWarning(category uint32, message string) {
	if app == nil {
		return
	}
	for {
		current := atomic.LoadUint32(&app.securityWarningBits)
		if current&category != 0 {
			return
		}
		if atomic.CompareAndSwapUint32(&app.securityWarningBits, current, current|category) {
			if app.log != nil {
				app.log.Warning(message)
			}
			app.recordStartupError(fmt.Errorf("生产环境安全检查失败: %s", message))
			return
		}
	}
}
