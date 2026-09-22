package framework

import (
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
	"sync/atomic"
)

// serviceResources 只记录资源所有权；当前服务实例始终由容器保存。
type serviceResources struct {
	mu      sync.Mutex
	entries []*ownedServiceResource
	closing map[string]bool
	closed  bool
}

type ownedServiceResource struct {
	name     string
	instance io.Closer
	once     sync.Once
	err      error
	closed   atomic.Bool
}

func (resource *ownedServiceResource) close() error {
	resource.once.Do(func() {
		resource.err = safeResourceClose(resource.name, resource.instance.Close)
		if resource.err != nil {
			resource.err = fmt.Errorf("关闭服务 %q 失败: %w", resource.name, resource.err)
		}
		resource.closed.Store(true)
	})
	return resource.err
}

// track 只接管类型正确的内置资源；重复登记同一对象不会重复关闭。
func (resources *serviceResources) track(name string, instance any) error {
	// 应用本身是所有者，不能再次作为自己的待关闭资源登记。
	if name == serviceKeyApp || name == serviceKeyContainer {
		return nil
	}
	expected, managed := managedServiceType(name)
	if !managed || isNilServiceInstance(instance) || expected != reflect.TypeOf(instance) {
		return nil
	}
	closer, closable := instance.(io.Closer)
	if !closable {
		return nil
	}
	resources.mu.Lock()
	for _, existing := range resources.entries {
		if existing.instance == closer {
			closing := resources.closed || resources.closing[name]
			resources.mu.Unlock()
			if closing {
				return ErrApplicationClosed
			}
			if existing.closed.Load() {
				return fmt.Errorf("%w: 服务 %q 的资源已经关闭", ErrServiceUnavailable, name)
			}
			return nil
		}
	}
	resource := &ownedServiceResource{name: name, instance: closer}
	if resources.closed || resources.closing[name] {
		resources.mu.Unlock()
		return errors.Join(ErrApplicationClosed, resource.close())
	}
	resources.entries = append(resources.entries, resource)
	resources.mu.Unlock()
	return nil
}

// close 先封闭资源接管窗口，再按创建顺序的逆序释放，外部 I/O 不占用登记锁。
func (resources *serviceResources) close(names ...string) error {
	resources.mu.Lock()
	selected := make(map[string]bool, len(names))
	if len(names) == 0 {
		resources.closed = true
	}
	if resources.closing == nil {
		resources.closing = make(map[string]bool)
	}
	for _, name := range names {
		selected[name], resources.closing[name] = true, true
	}
	entries := append([]*ownedServiceResource(nil), resources.entries...)
	resources.mu.Unlock()
	var result error
	for index := len(entries) - 1; index >= 0; index-- {
		if !entries[index].closed.Load() && (len(names) == 0 || selected[entries[index].name]) {
			result = errors.Join(result, entries[index].close())
		}
	}
	return result
}

// retire 提前释放已被替换且无需保留到关机的资源，后续统一关闭不会再处理它。
func (resources *serviceResources) retire(instance io.Closer) error {
	resources.mu.Lock()
	var target *ownedServiceResource
	for _, entry := range resources.entries {
		if entry.instance == instance {
			target = entry
			break
		}
	}
	resources.mu.Unlock()
	if target == nil {
		return fmt.Errorf("%w: 资源没有登记", ErrServiceUnavailable)
	}
	return target.close()
}

// installManagedService 在 Provider 创建资源后立即接管，包括后续绑定失败的资源。
func (app *App) installManagedService(name string, instance any) error {
	if app == nil {
		return ErrNilApplication
	}
	if err := app.resources.track(name, instance); err != nil {
		return err
	}
	return app.Instance(name, instance)
}

func (app *App) closeServiceResources(names ...string) error {
	if app == nil {
		return nil
	}
	return app.resources.close(names...)
}
