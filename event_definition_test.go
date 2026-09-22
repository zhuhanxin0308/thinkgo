package framework

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/framework/event"
	"github.com/zhuhanxin0308/thinkgo/framework/migration"
)

const eventRegistrationTestTimeout = time.Second

type thinkPHPEventSubscriber struct {
	steps *[]string
}

type eventDefinitionMigrationSubscriber struct {
	application *App
	migration   migration.Migration
}

func (subscriber *eventDefinitionMigrationSubscriber) Subscribe(*event.Dispatcher) error {
	return subscriber.application.RegisterMigration(subscriber.migration)
}

type blockingEventDefinitionSubscriber struct {
	entered chan struct{}
	release chan struct{}
}

func (subscriber *blockingEventDefinitionSubscriber) Subscribe(dispatcher *event.Dispatcher) error {
	if err := dispatcher.Listen("plugin.concurrent", &event.SimpleListener{}); err != nil {
		return err
	}
	close(subscriber.entered)
	<-subscriber.release
	return nil
}

// TestAppLoadEventRollsBackWholeDefinition 验证 bind、listen、subscribe 作为一个
// 完整事务提交，末尾订阅失败不会泄漏前面已经暂存的别名或监听器。
func TestAppLoadEventRollsBackWholeDefinition(t *testing.T) {
	application := NewApp(t.TempDir())
	aliasCalls := 0
	definitionCalls := 0
	if err := application.Event().Listen("plugin.alias", &event.SimpleListener{Handler: func(event.Event) error {
		aliasCalls++
		return nil
	}}); err != nil {
		t.Fatalf("注册事务前监听器失败: %v", err)
	}

	definition := EventDefinition{
		Bind: map[string]event.Factory{
			"plugin.alias": func(data interface{}) event.Event {
				return event.NewEvent("plugin.target", data)
			},
		},
		Listen: map[string][]event.Listener{
			"plugin.target": {
				&event.SimpleListener{Handler: func(event.Event) error {
					definitionCalls++
					return nil
				}},
			},
		},
		Subscribe: []event.Subscriber{nil},
	}
	if err := application.LoadEvent(definition); !errors.Is(err, event.ErrInvalidSubscriber) {
		t.Fatalf("无效订阅者应使完整事件定义失败，实际为 %v", err)
	}
	if err := application.Event().Trigger("plugin.alias", nil); err != nil {
		t.Fatalf("回滚后触发原始别名事件失败: %v", err)
	}
	if aliasCalls != 1 || definitionCalls != 0 {
		t.Fatalf("失败定义不得泄漏别名或监听器，alias=%d definition=%d", aliasCalls, definitionCalls)
	}
}

// TestAppLoadEventHonorsRegistrationWindow 验证运行时冻结后不能再装载事件定义，
// 使事件扩展与路由、控制器和中间件遵守同一注册边界。
func TestAppLoadEventHonorsRegistrationWindow(t *testing.T) {
	application := NewApp(t.TempDir())
	application.closeApplicationRegistration()
	err := application.LoadEvent(EventDefinition{
		Listen: map[string][]event.Listener{
			"plugin.late": {&event.SimpleListener{Handler: func(event.Event) error { return nil }}},
		},
	})
	if !errors.Is(err, ErrApplicationRegistrationClosed) {
		t.Fatalf("注册窗口关闭后装载事件必须失败，实际为 %v", err)
	}
	if application.Event().HasListener("plugin.late") {
		t.Fatal("被拒绝的运行时事件定义不得留下监听器")
	}
}

// TestAppLoadEventAllowsSubscriberRegistrationReentry 验证事件订阅者可以继续使用
// 应用公开注册 API；LoadEvent 不能在执行扩展回调时持有注册窗口读锁而自锁。
func TestAppLoadEventAllowsSubscriberRegistrationReentry(t *testing.T) {
	application := NewApp(t.TempDir())
	registeredMigration, err := migration.New(
		"202609010001_event_subscriber",
		strings.Repeat("a", 64),
		func(context.Context, migration.Executor) error { return nil },
		func(context.Context, migration.Executor) error { return nil },
	)
	if err != nil {
		t.Fatalf("创建事件订阅者迁移失败: %v", err)
	}

	result := make(chan error, 1)
	go func() {
		result <- application.LoadEvent(EventDefinition{
			Subscribe: []event.Subscriber{&eventDefinitionMigrationSubscriber{
				application: application,
				migration:   registeredMigration,
			}},
		})
	}()
	select {
	case loadErr := <-result:
		if loadErr != nil {
			t.Fatalf("事件订阅者重入应用注册失败: %v", loadErr)
		}
	case <-time.After(eventRegistrationTestTimeout):
		t.Fatal("LoadEvent 在订阅者重入应用注册时发生死锁")
	}
}

// TestAppLoadEventRechecksRegistrationWindowBeforeCommit 验证用户回调执行期间
// 注册窗口若被关闭，暂存事件不会越过最终提交保护泄漏到运行期。
func TestAppLoadEventRechecksRegistrationWindowBeforeCommit(t *testing.T) {
	application := NewApp(t.TempDir())
	subscriber := &blockingEventDefinitionSubscriber{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	result := make(chan error, 1)
	go func() {
		result <- application.LoadEvent(EventDefinition{
			Subscribe: []event.Subscriber{subscriber},
		})
	}()
	select {
	case <-subscriber.entered:
	case <-time.After(eventRegistrationTestTimeout):
		t.Fatal("事件订阅者未进入暂存阶段")
	}
	application.closeApplicationRegistration()
	close(subscriber.release)
	select {
	case loadErr := <-result:
		if !errors.Is(loadErr, ErrApplicationRegistrationClosed) {
			t.Fatalf("关闭窗口后的事件提交应被拒绝，实际为 %v", loadErr)
		}
	case <-time.After(eventRegistrationTestTimeout):
		t.Fatal("关闭注册窗口后事件事务未返回")
	}
	if application.Event().HasListener("plugin.concurrent") {
		t.Fatal("关闭注册窗口后的暂存监听器不得泄漏")
	}
}

func (subscriber *thinkPHPEventSubscriber) Subscribe(dispatcher *event.Dispatcher) error {
	return dispatcher.Listen("OrderPaid", &event.SimpleListener{Handler: func(event.Event) error {
		*subscriber.steps = append(*subscriber.steps, "subscriber")
		return nil
	}})
}

// TestAppLoadEventLoadsDeclarativeThinkPHPDefinition 验证 app/event.go 对应的
// bind、listen、subscribe 三个区块会一次性装配到事件调度器。
func TestAppLoadEventLoadsDeclarativeThinkPHPDefinition(t *testing.T) {
	application := NewApp(t.TempDir())
	steps := make([]string, 0, 2)
	definition := EventDefinition{
		Bind: map[string]event.Factory{
			"order.paid": func(data interface{}) event.Event {
				return event.NewEvent("OrderPaid", data)
			},
		},
		Listen: map[string][]event.Listener{
			"OrderPaid": {
				&event.SimpleListener{Handler: func(event.Event) error {
					steps = append(steps, "listener")
					return nil
				}},
			},
		},
		Subscribe: []event.Subscriber{&thinkPHPEventSubscriber{steps: &steps}},
	}
	if err := application.LoadEvent(definition); err != nil {
		t.Fatalf("加载声明式事件失败: %v", err)
	}
	if err := application.Event().Trigger("order.paid", map[string]interface{}{"id": 8}); err != nil {
		t.Fatalf("触发绑定事件失败: %v", err)
	}
	if want := []string{"listener", "subscriber"}; !reflect.DeepEqual(steps, want) {
		t.Fatalf("事件执行顺序错误: want=%#v got=%#v", want, steps)
	}
}
