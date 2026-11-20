package framework

import (
	"errors"
	"fmt"
)

// registerApplicationServiceProvider 在 Provider 生命周期内预留注册，回调成功后再发布新实例。
// 初始化阶段尚未开始时，新的 Provider 仍由常规应用初始化流程调度。
func (app *App) registerApplicationServiceProvider(candidate, previous *applicationServiceProvider) error {
	lifecycle := &app.providers
	lifecycle.lock.Lock()
	lifecycle.initializeLocked()
	if app.providerRegistrationClosedLocked() {
		lifecycle.lock.Unlock()
		return ErrProviderRegistrationClosed
	}
	var existing *providerRegistration
	if previous != nil {
		for _, registration := range lifecycle.providers {
			if registration.provider == previous {
				existing = registration
				break
			}
		}
		if existing == nil {
			lifecycle.lock.Unlock()
			return fmt.Errorf("%w: 应用服务 %T 缺少生命周期记录", ErrInvalidProvider, previous.service)
		}
		if lifecycle.initializing || existing.initializing {
			lifecycle.lock.Unlock()
			return fmt.Errorf("%w: 应用正在初始化，暂不能替换 %T", ErrApplicationServiceRegistrationInProgress, previous.service)
		}
		if previous.service == candidate.service {
			lifecycle.lock.Unlock()
			return nil
		}
	} else {
		// 先预留启动顺序，但在回调完成前不发布此 Provider。
		existing = &providerRegistration{provider: candidate}
		lifecycle.providers = append(lifecycle.providers, existing)
	}
	published := false
	awaitingPhase := false
	lifecycle.registering++
	lifecycle.lock.Unlock()
	defer func() {
		lifecycle.lock.Lock()
		if previous == nil && !published {
			lifecycle.removeRegistrationLocked(existing)
		}
		if awaitingPhase {
			lifecycle.applicationPhases--
		} else {
			lifecycle.registering--
		}
		lifecycle.condition.Broadcast()
		lifecycle.lock.Unlock()
	}()

	if err := safeProviderRegister(candidate, app); err != nil {
		wrapped := fmt.Errorf("应用服务 %T 注册失败: %w", candidate.service, err)
		if previous == nil {
			app.recordStartupError(wrapped)
		}
		return wrapped
	}
	if previous == nil {
		published = app.publishApplicationService(candidate, previous, existing, false)
		if published {
			return nil
		}
	}

	// 首次初始化持有阶段锁时可能在等待外层 Register 回调。初始阶段的替换必须
	// 在转入阶段队列前取得锁，否则同步嵌套注册会让两者相互等待。
	lifecycle.lock.Lock()
	phaseLocked := previous != nil && !lifecycle.initialized
	if phaseLocked && !lifecycle.phaseMu.TryLock() {
		lifecycle.lock.Unlock()
		return fmt.Errorf("%w: 生命周期阶段正在执行，暂不能替换 %T", ErrApplicationServiceRegistrationInProgress, previous.service)
	}
	awaitingPhase = true
	lifecycle.registering--
	lifecycle.applicationPhases++
	lifecycle.condition.Broadcast()
	lifecycle.lock.Unlock()
	if !phaseLocked {
		lifecycle.phaseMu.Lock()
	}
	defer lifecycle.phaseMu.Unlock()

	published = app.publishApplicationService(candidate, previous, existing, false)
	if !published {
		if err := safeProviderInitialize(candidate, app); err != nil {
			wrapped := fmt.Errorf("应用服务 %T 初始化失败: %w", candidate.service, errors.Join(err, safeProviderShutdown(candidate, app)))
			if previous == nil {
				lifecycle.lock.Lock()
				lifecycle.initializeErr = errors.Join(lifecycle.initializeErr, wrapped)
				lifecycle.lock.Unlock()
				app.recordStartupError(wrapped)
			}
			return wrapped
		}
		published = app.publishApplicationService(candidate, previous, existing, true)
	}

	if previous != nil {
		if err := safeProviderShutdown(previous, app); err != nil {
			return fmt.Errorf("应用服务 %T 已替换，释放旧实例失败: %w", previous.service, err)
		}
	}
	return nil
}

// publishApplicationService 同时更新公开查询与生命周期记录，替换时沿用原启动位置。
// 返回 false 表示当前生命周期需要先初始化候选实例。
func (app *App) publishApplicationService(candidate, previous *applicationServiceProvider, existing *providerRegistration, initialized bool) bool {
	lifecycle := &app.providers
	lifecycle.lock.Lock()
	defer lifecycle.lock.Unlock()
	if !initialized && (lifecycle.initialized || existing.initialized) {
		return false
	}
	app.applicationServiceMu.Lock()
	defer app.applicationServiceMu.Unlock()

	identity, _ := identifyProvider(candidate)
	lifecycle.identities[identity] = struct{}{}
	existing.provider = candidate
	existing.registered = true
	existing.initialized = initialized
	if previous == nil {
		app.applicationServices = append(app.applicationServices, candidate)
		return true
	}
	previousIdentity, _ := identifyProvider(previous)
	delete(lifecycle.identities, previousIdentity)
	for index, current := range app.applicationServices {
		if current == previous {
			app.applicationServices[index] = candidate
			break
		}
	}
	return true
}
