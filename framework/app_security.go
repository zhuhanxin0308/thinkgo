package framework

import (
	"fmt"
	"net/http"

	"thinkgo/framework/context"
	"thinkgo/framework/cookie"
	"thinkgo/framework/middleware"
	"thinkgo/framework/session"
	sessionDriver "thinkgo/framework/session/driver"
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
	case "memory":
		backend = sessionDriver.NewMemory()
	case "file":
		config.StoragePath, err = normalizeAppStoragePath(app.BasePath, config.StoragePath)
		if err != nil {
			return nil, fmt.Errorf("解析 Session 存储路径失败: %w", err)
		}
		backend, err = sessionDriver.NewFile(config.StoragePath)
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
	manager, _ := session.NewSessionWithConfig(config, sessionDriver.NewMemory(), cookieFactory)
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
