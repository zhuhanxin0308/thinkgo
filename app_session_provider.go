package framework

import (
	"errors"
	"fmt"
	"time"
)

const defaultSessionGarbageCollectionInterval = time.Hour

// appSessionProvider 负责在 Cookie 和日志装配完成后创建 Session 管理器。
type appSessionProvider struct {
	stopGarbageCollector func()
}

// Register 保留 Provider 注册阶段，Session 驱动必须等待最终配置和 Cookie 工厂完成后创建。
func (provider *appSessionProvider) Register(app *App) error {
	return nil
}

// Initialize 根据配置创建 Session 管理器，并按配置启动后台回收任务。
func (provider *appSessionProvider) Initialize(app *App) error {
	if app == nil {
		return ErrNilApplication
	}
	if app.config == nil {
		return errors.New("应用配置实例不能为空")
	}
	if app.container == nil {
		return errors.New("应用容器实例不能为空")
	}

	initializeErrors := make([]error, 0, 3)
	if app.cookie == nil {
		app.cookie = fallbackAppCookie()
		if err := app.Instance(serviceKeyCookie, app.cookie); err != nil {
			initializeErrors = append(initializeErrors, fmt.Errorf("绑定 Session 退化 Cookie 服务失败: %w", err))
		}
		initializeErrors = append(initializeErrors, errors.New("session 依赖的 Cookie 实例不能为空"))
	}

	configuredSession, err := createAppSession(app, app.config.GetMap("session"), app.cookie)
	if err != nil {
		configuredSession = fallbackAppSession(app.cookie)
		initializeErrors = append(initializeErrors, fmt.Errorf("初始化 Session 失败: %w", err))
	}
	if configuredSession == nil {
		return errors.Join(append(initializeErrors, errors.New("无法创建 Session 退化实例"))...)
	}
	configuredSession.SetLogger(app.log)
	if err := app.installManagedService(serviceKeySession, configuredSession); err != nil {
		initializeErrors = append(initializeErrors, fmt.Errorf("绑定 Session 服务失败: %w", err))
		return errors.Join(initializeErrors...)
	}
	if app.sessionEnabled {
		provider.stopGarbageCollector = configuredSession.StartGarbageCollector(defaultSessionGarbageCollectionInterval)
	}
	return errors.Join(initializeErrors...)
}

// Boot 不在 Provider 启动阶段重复创建 Session 或回收任务。
func (provider *appSessionProvider) Boot(app *App) error {
	return nil
}

// Shutdown 停止 Provider 启动的 Session 后台回收任务。
func (provider *appSessionProvider) Shutdown(app *App) error {
	if provider == nil {
		return nil
	}
	var result error
	if provider.stopGarbageCollector != nil {
		result = safeStopSessionGarbageCollector(provider.stopGarbageCollector)
		provider.stopGarbageCollector = nil
	}
	return errors.Join(result, app.closeServiceResources(serviceKeySession))
}
