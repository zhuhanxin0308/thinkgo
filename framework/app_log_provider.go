package framework

import (
	"errors"
	"fmt"
	"strings"

	"thinkgo/framework/log"
)

const defaultAppLogChannel = "file"

// appLogProvider 负责在配置完成后装配应用日志及其通道。
type appLogProvider struct {
	owned *log.Log
}

// Register 保留 Provider 注册阶段，日志通道必须等待最终配置完成后创建。
func (provider *appLogProvider) Register(app *App) error {
	return nil
}

// Initialize 根据配置创建默认日志通道和附加通道。
func (provider *appLogProvider) Initialize(app *App) error {
	if app == nil {
		return ErrNilApplication
	}
	if app.config == nil {
		return errors.New("应用配置实例不能为空")
	}
	if app.container == nil {
		return errors.New("应用容器实例不能为空")
	}
	if app.log == nil {
		return errors.New("应用日志实例不能为空")
	}
	provider.owned = app.log

	initializeErrors := make([]error, 0)
	logConfig := app.config.GetMap("log")
	defaultChannel := defaultAppLogChannel
	if rawDefault, exists := logConfig["default"]; exists {
		if value, ok := rawDefault.(string); ok && strings.TrimSpace(value) != "" {
			defaultChannel = strings.TrimSpace(value)
		} else {
			initializeErrors = append(initializeErrors, errors.New("日志配置 default 必须是非空字符串"))
		}
	}

	channels, channelsValid := logConfig["channels"].(map[string]interface{})
	if !channelsValid {
		initializeErrors = append(initializeErrors, errors.New("日志配置 channels 必须是对象"))
	} else {
		rawDefaultChannel, exists := channels[defaultChannel]
		channelConfig, configValid := rawDefaultChannel.(map[string]interface{})
		if !exists || !configValid {
			initializeErrors = append(initializeErrors, fmt.Errorf("默认日志通道 %q 不存在或配置无效", defaultChannel))
		} else {
			configuredLog, err := createAppLogChannel(app, channelConfig, app.DebugMode)
			if err != nil {
				initializeErrors = append(initializeErrors, fmt.Errorf("初始化默认日志通道 %q 失败: %w", defaultChannel, err))
			} else {
				previousLog := app.log
				app.log = configuredLog
				provider.owned = configuredLog
				if previousLog != nil && previousLog != configuredLog {
					if err := previousLog.Close(); err != nil {
						initializeErrors = append(initializeErrors, fmt.Errorf("关闭初始日志通道失败: %w", err))
					}
				}
			}
		}
	}
	if channelsValid && app.log != nil {
		for name, rawChannelConfig := range channels {
			if name == defaultChannel {
				continue
			}
			channelConfig, ok := rawChannelConfig.(map[string]interface{})
			if !ok {
				initializeErrors = append(initializeErrors, fmt.Errorf("日志通道 %q 的配置必须是对象", name))
				continue
			}
			channel, err := createAppLogChannel(app, channelConfig, false)
			if err != nil {
				initializeErrors = append(initializeErrors, fmt.Errorf("初始化日志通道 %q 失败: %w", name, err))
				continue
			}
			if err := app.log.RegisterChannel(name, channel); err != nil {
				_ = channel.Close()
				initializeErrors = append(initializeErrors, fmt.Errorf("注册日志通道 %q 失败: %w", name, err))
			}
		}
	}

	app.Instance(serviceKeyLog, app.log)
	return errors.Join(initializeErrors...)
}

// Boot 不在 Provider 启动阶段重复创建日志通道。
func (provider *appLogProvider) Boot(app *App) error {
	return nil
}

// Shutdown 释放 Provider 创建的日志，以及当前容器中可能替换过的日志实例。
func (provider *appLogProvider) Shutdown(app *App) error {
	if provider == nil {
		return nil
	}
	var current *log.Log
	if instance := serviceInstanceForShutdown(app, ServiceLog); instance != nil {
		current, _ = instance.(*log.Log)
	}
	return closeApplicationLogs(provider.owned, appLogSnapshot(app), current)
}
