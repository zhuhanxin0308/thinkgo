package framework

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	defaultRequestTaskShutdownTimeout = 5 * time.Second
	operationalRequestTaskCapacity    = 16
)

var (
	// ErrRequestTaskCapacity 表示请求与后台收尾共用的有限容量已经耗尽。
	ErrRequestTaskCapacity = errors.New("请求生命周期容量已满")
	// ErrRequestTasksPending 表示停机预算内仍有任务使用应用依赖，关闭可在任务结束后重试。
	ErrRequestTasksPending = errors.New("仍有请求生命周期任务未完成")
)

// RequestTasksSnapshot 暴露实际存活任务和累计拒绝、完成、停机超时数量。
type RequestTasksSnapshot struct {
	Active           int
	Operational      int
	Completed        uint64
	Rejected         uint64
	ShutdownTimeouts uint64
}

type requestTaskRegistry struct {
	mu       sync.Mutex
	snapshot RequestTasksSnapshot
	closed   bool
	idle     chan struct{}
	timeout  time.Duration
}

// RequestTask 绑定请求执行和真实收尾的同一租约；Release 可以安全重复调用。
type RequestTask struct {
	registry    *requestTaskRegistry
	operational bool
	once        sync.Once
}

// AcquireRequestTask 在进入业务前预约一个有限槽位，收尾超时不会提前归还槽位。
func (app *App) AcquireRequestTask(capacity int, shutdownTimeout time.Duration) (*RequestTask, error) {
	return app.acquireRequestTask(capacity, shutdownTimeout, false)
}

// AcquireOperationalRequestTask 为框架运维端点提供独立的有限容量，仍然参与相同的停机屏障。
func (app *App) AcquireOperationalRequestTask(shutdownTimeout time.Duration) (*RequestTask, error) {
	return app.acquireRequestTask(operationalRequestTaskCapacity, shutdownTimeout, true)
}

func (app *App) acquireRequestTask(capacity int, timeout time.Duration, operational bool) (*RequestTask, error) {
	if app == nil {
		return nil, ErrNilApplication
	}
	if capacity <= 0 || timeout <= 0 {
		return nil, fmt.Errorf("请求任务容量和关闭预算必须为正数")
	}
	registry := &app.lifecycle.tasks
	registry.mu.Lock()
	defer registry.mu.Unlock()
	app.lifecycle.lock.Lock()
	closed := app.lifecycle.closed
	app.lifecycle.lock.Unlock()
	if registry.closed || closed {
		registry.snapshot.Rejected++
		return nil, ErrApplicationClosed
	}
	active := registry.snapshot.Active - registry.snapshot.Operational
	if operational {
		active = registry.snapshot.Operational
	}
	if active >= capacity {
		registry.snapshot.Rejected++
		return nil, ErrRequestTaskCapacity
	}
	registry.timeout = timeout
	registry.snapshot.Active++
	if operational {
		registry.snapshot.Operational++
	}
	return &RequestTask{registry: registry, operational: operational}, nil
}

// Release 只在业务执行和全部后台清理实际返回之后归还容量。
func (task *RequestTask) Release() {
	if task == nil || task.registry == nil {
		return
	}
	task.once.Do(func() {
		registry := task.registry
		registry.mu.Lock()
		defer registry.mu.Unlock()
		registry.snapshot.Active--
		if task.operational {
			registry.snapshot.Operational--
		}
		registry.snapshot.Completed++
		if registry.snapshot.Active == 0 && registry.idle != nil {
			close(registry.idle)
			registry.idle = nil
		}
	})
}

// RequestTaskSnapshot 返回当前应用的并发安全观测快照。
func (app *App) RequestTaskSnapshot() RequestTasksSnapshot {
	if app == nil {
		return RequestTasksSnapshot{}
	}
	registry := &app.lifecycle.tasks
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return registry.snapshot
}

// WaitRequestTasks 等待实际任务排空，不关闭服务接收入口。
func (app *App) WaitRequestTasks(ctx context.Context) error {
	if app == nil {
		return ErrNilApplication
	}
	if ctx == nil {
		return ErrInvalidContainerResolutionContext
	}
	registry := &app.lifecycle.tasks
	registry.mu.Lock()
	if registry.snapshot.Active == 0 {
		registry.mu.Unlock()
		return nil
	}
	if registry.idle == nil {
		registry.idle = make(chan struct{})
	}
	idle := registry.idle
	registry.mu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%w: active=%d: %w", ErrRequestTasksPending, app.RequestTaskSnapshot().Active, ctx.Err())
	}
}

// drainRequestTasksForClose 先封闭整个应用集合，再以共同预算排空，避免部分关闭破坏重试入口。
func (app *App) drainRequestTasksForClose() error {
	// 注册锁同时保护目录指针的发布，先封闭注册可避免排空快照之后出现全新目录。
	app.closeApplicationRegistration()
	applications := []*App{app}
	if catalog := app.applicationCatalog; catalog != nil {
		catalog.lock.Lock()
		// BuildApplications 全程持有同一目录锁；尚未开始的构造必须永久拒绝。
		if !catalog.built {
			catalog.built = true
			catalog.buildErr = errors.Join(catalog.buildErr, ErrApplicationClosed)
		}
		for _, candidate := range catalog.applications {
			if candidate != nil && candidate != app {
				applications = append(applications, candidate)
			}
		}
		catalog.lock.Unlock()
	}
	var timeout time.Duration
	for _, candidate := range applications {
		registry := &candidate.lifecycle.tasks
		registry.mu.Lock()
		registry.closed = true
		if registry.timeout > timeout {
			timeout = registry.timeout
		}
		registry.mu.Unlock()
	}
	if timeout <= 0 {
		timeout = defaultRequestTaskShutdownTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var waitErr error
	for _, candidate := range applications {
		if err := candidate.WaitRequestTasks(ctx); err != nil {
			registry := &candidate.lifecycle.tasks
			registry.mu.Lock()
			registry.snapshot.ShutdownTimeouts++
			registry.mu.Unlock()
			waitErr = errors.Join(waitErr, err)
		}
	}
	return waitErr
}
