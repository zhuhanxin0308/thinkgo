package framework

import (
	"fmt"

	"github.com/zhuhanxin0308/thinkgo/v3/event"
)

// EventDefinition 对应 ThinkPHP app/event.php 的 bind、listen、subscribe 三段配置。
type EventDefinition struct {
	Bind      map[string]event.Factory
	Listen    map[string][]event.Listener
	Subscribe []event.Subscriber
}

// LoadEvent 将应用事件定义装配到事件管理器，对应 ThinkPHP App.loadEvent。
func (app *App) LoadEvent(definition EventDefinition) error {
	if app == nil {
		return ErrNilApplication
	}
	app.registrationMu.RLock()
	registrationErr := app.ensureRegistrationOpen()
	applicationScoped := app.loadingNativeDefinition
	app.registrationMu.RUnlock()
	if registrationErr != nil {
		return registrationErr
	}
	if app.event == nil {
		return fmt.Errorf("应用事件管理器尚未创建")
	}
	acquireCommitGuard := func() (func(), error) {
		app.registrationMu.RLock()
		if err := app.ensureRegistrationOpen(); err != nil {
			app.registrationMu.RUnlock()
			return nil, err
		}
		return app.registrationMu.RUnlock, nil
	}
	return app.event.RegisterTransactionWithCommitGuard(func(staged *event.Dispatcher) error {
		if len(definition.Bind) > 0 {
			if err := staged.Bind(definition.Bind); err != nil {
				return fmt.Errorf("加载事件别名失败: %w", err)
			}
		}
		if len(definition.Listen) > 0 {
			var err error
			if applicationScoped {
				err = staged.ListenApplicationEvents(definition.Listen)
			} else {
				err = staged.ListenEvents(definition.Listen)
			}
			if err != nil {
				return fmt.Errorf("加载事件监听器失败: %w", err)
			}
		}
		for index, subscriber := range definition.Subscribe {
			var err error
			if applicationScoped {
				err = staged.SubscribeApplication(subscriber)
			} else {
				err = staged.Subscribe(subscriber)
			}
			if err != nil {
				return fmt.Errorf("加载第 %d 个事件订阅者失败: %w", index+1, err)
			}
		}
		return nil
	}, acquireCommitGuard)
}
