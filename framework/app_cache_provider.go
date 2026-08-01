package framework

import (
	"errors"
	"fmt"

	"thinkgo/framework/cache"
)

// appCacheProvider 负责在配置加载完成后装配应用级缓存服务。
type appCacheProvider struct {
	owned *cache.Cache
}

// Register 保留 Provider 注册阶段，缓存实例必须等待配置完成后才能创建。
func (provider *appCacheProvider) Register(app *App) error {
	return nil
}

// Initialize 根据最终配置创建缓存管理器，并安装到 App 和容器。
func (provider *appCacheProvider) Initialize(app *App) error {
	if app == nil {
		return ErrNilApplication
	}
	if app.config == nil {
		return errors.New("应用配置实例不能为空")
	}
	if app.container == nil {
		return errors.New("应用容器实例不能为空")
	}

	configuredCache, err := createAppCache(app, app.config.GetMap("cache"))
	if err != nil {
		// 保留非空服务实例，但不安装隐式驱动；任何缓存调用都会返回明确错误。
		app.cache = cache.NewCache(nil, nil)
		provider.owned = app.cache
		app.Instance(serviceKeyCache, app.cache)
		return fmt.Errorf("初始化缓存失败: %w", err)
	}
	app.cache = configuredCache
	provider.owned = configuredCache
	app.Instance(serviceKeyCache, app.cache)
	return nil
}

// Boot 不在 Provider 启动阶段重复创建缓存实例。
func (provider *appCacheProvider) Boot(app *App) error {
	return nil
}

// Shutdown 释放 Provider 创建的缓存，以及当前容器中可能替换过的缓存实例。
func (provider *appCacheProvider) Shutdown(app *App) error {
	if provider == nil {
		return nil
	}
	var current *cache.Cache
	if instance := serviceInstanceForShutdown(app, ServiceCache); instance != nil {
		current, _ = instance.(*cache.Cache)
	}
	return closeApplicationCaches(provider.owned, appCacheSnapshot(app), current)
}
