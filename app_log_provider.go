package framework

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/framework/log"
)

const defaultAppLogChannel = "file"

// appLogProvider 负责在配置完成后装配应用日志及其通道。
type appLogProvider struct{}

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
	if err := app.installManagedService(serviceKeyLog, app.log); err != nil {
		return err
	}

	initializeErrors := make([]error, 0)
	logConfig := app.config.GetMap("log")
	allowedTopLevel := map[string]bool{
		"default": true, "level": true, "type_channel": true,
		"close": true, "processor": true, "channels": true,
	}
	unknown := make([]string, 0)
	for name := range logConfig {
		if !allowedTopLevel[name] {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	for _, name := range unknown {
		initializeErrors = append(initializeErrors, fmt.Errorf("日志配置包含未知字段 %q", name))
	}
	globalLevels, levelsErr := readLogLevels(logConfig["level"])
	if levelsErr != nil {
		initializeErrors = append(initializeErrors, levelsErr)
	}
	closeLogging := false
	if rawClose, exists := logConfig["close"]; exists {
		var ok bool
		closeLogging, ok = rawClose.(bool)
		if !ok {
			initializeErrors = append(initializeErrors, errors.New("日志配置 close 必须是布尔值"))
		}
	}
	if rawProcessor, exists := logConfig["processor"]; exists && rawProcessor != nil {
		initializeErrors = append(initializeErrors, errors.New("日志配置 processor 必须在 Go 服务代码中注册，JSON 默认值只能为 null"))
	}
	if rawRouting, exists := logConfig["type_channel"]; exists {
		routing, ok := rawRouting.(map[string]interface{})
		if !ok {
			initializeErrors = append(initializeErrors, errors.New("日志配置 type_channel 必须是对象"))
		} else if len(routing) > 0 {
			initializeErrors = append(initializeErrors, errors.New("日志配置 type_channel 尚无等价的通道路由实现"))
		}
	}
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
	} else if closeLogging {
		previousLog := app.log
		if err := app.installManagedService(serviceKeyLog, log.NewLog()); err != nil {
			return err
		}
		if previousLog != nil && previousLog != app.log {
			if err := app.resources.retire(previousLog); err != nil {
				initializeErrors = append(initializeErrors, fmt.Errorf("关闭初始日志通道失败: %w", err))
			}
		}
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
				if err := app.installManagedService(serviceKeyLog, configuredLog); err != nil {
					return err
				}
				if previousLog != nil && previousLog != configuredLog {
					if err := app.resources.retire(previousLog); err != nil {
						initializeErrors = append(initializeErrors, fmt.Errorf("关闭初始日志通道失败: %w", err))
					}
				}
			}
		}
	}
	if levelsErr == nil && app.log != nil {
		app.log.SetLevels(globalLevels)
	}
	if channelsValid && !closeLogging && app.log != nil {
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

	if err := app.Instance(serviceKeyLog, app.log); err != nil {
		initializeErrors = append(initializeErrors, fmt.Errorf("绑定日志服务失败: %w", err))
	}
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
	return app.closeServiceResources(serviceKeyLog)
}
