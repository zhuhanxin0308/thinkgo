package framework

import (
	"errors"
	"fmt"

	"github.com/zhuhanxin0308/thinkgo/framework/cache"
)

// appCacheProvider 负责在配置加载完成后装配应用级缓存服务。
type appCacheProvider struct{}

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
		bindErr := app.installManagedService(serviceKeyCache, cache.NewCache(nil, nil))
		return errors.Join(
			fmt.Errorf("初始化缓存失败: %w", err),
			wrapServiceBindingError(serviceKeyCache, bindErr),
		)
	}
	return wrapServiceBindingError(serviceKeyCache, app.installManagedService(serviceKeyCache, configuredCache))
}

// Boot 不在 Provider 启动阶段重复创建缓存实例。
func (provider *appCacheProvider) Boot(app *App) error {
	return nil
}

// Shutdown 按生命周期顺序释放登记的全部缓存资源。
func (provider *appCacheProvider) Shutdown(app *App) error {
	if provider == nil {
		return nil
	}
	return app.closeServiceResources(serviceKeyCache)
}
