package framework

import (
	"errors"
	"fmt"
)

var (
	// ErrProtectedServiceMutation 表示变更会破坏应用内置服务与字段快照的一致性。
	ErrProtectedServiceMutation = errors.New("应用内置服务不允许此变更")
	// ErrContainerOwnershipConflict 表示同一容器共享状态被错误地归属给多个应用。
	ErrContainerOwnershipConflict = errors.New("容器已经归属其他应用")
)

type containerMutationType uint8

const (
	containerMutationBind containerMutationType = iota
	containerMutationInstance
	containerMutationDelete
)

// containerMutation 描述一次不可拆分的容器变更，应用门禁据此先校验再提交。
type containerMutation struct {
	typeName  containerMutationType
	abstract  string
	concrete  interface{}
	lifecycle LifecycleType
	internal  bool
}

// TryBind 注册单例绑定，并把归属应用的生命周期拒绝作为错误返回。
func (c *Container) TryBind(abstract string, concrete interface{}) error {
	return c.tryMutation(containerMutation{
		typeName:  containerMutationBind,
		abstract:  abstract,
		concrete:  concrete,
		lifecycle: Singleton,
	})
}

// TryBindFactory 注册工厂绑定，并把归属应用的生命周期拒绝作为错误返回。
func (c *Container) TryBindFactory(abstract string, concrete interface{}) error {
	return c.tryMutation(containerMutation{
		typeName:  containerMutationBind,
		abstract:  abstract,
		concrete:  concrete,
		lifecycle: Factory,
	})
}

// TryBindScoped 注册作用域绑定，并把归属应用的生命周期拒绝作为错误返回。
func (c *Container) TryBindScoped(abstract string, concrete interface{}) error {
	return c.tryMutation(containerMutation{
		typeName:  containerMutationBind,
		abstract:  abstract,
		concrete:  concrete,
		lifecycle: Scoped,
	})
}

// TryInstance 注册显式实例，并把归属应用的生命周期或类型拒绝作为错误返回。
func (c *Container) TryInstance(abstract string, instance interface{}) error {
	return c.tryMutation(containerMutation{
		typeName: containerMutationInstance,
		abstract: abstract,
		concrete: instance,
	})
}

// TryDelete 删除服务，并把归属应用的生命周期或内置服务保护错误返回。
func (c *Container) TryDelete(abstract string) error {
	return c.tryMutation(containerMutation{
		typeName: containerMutationDelete,
		abstract: abstract,
	})
}

func (c *Container) tryMutation(mutation containerMutation) error {
	if c == nil {
		return fmt.Errorf("容器不能为空")
	}
	state := c.sharedState()
	// 归属锁覆盖“读取 owner 到提交”的完整区间，避免容器接入应用时仍有裸写穿透。
	state.ownerLock.RLock()
	defer state.ownerLock.RUnlock()
	if state.owner != nil {
		return state.owner.applyContainerMutation(c, mutation)
	}
	c.applyMutationDirect(mutation)
	return nil
}

func (c *Container) applyMutationDirect(mutation containerMutation) {
	switch mutation.typeName {
	case containerMutationBind:
		c.bindDirect(mutation.abstract, mutation.concrete, mutation.lifecycle)
	case containerMutationInstance:
		c.instanceDirect(mutation.abstract, mutation.concrete)
	case containerMutationDelete:
		c.deleteDirect(mutation.abstract)
	default:
		panic(fmt.Errorf("未知容器变更类型: %d", mutation.typeName))
	}
}

// attachApplication 把容器共享状态绑定到唯一应用；重复绑定同一应用幂等。
func (c *Container) attachApplication(app *App) error {
	if c == nil {
		return fmt.Errorf("%w: 应用容器为空", ErrServiceUnavailable)
	}
	if app == nil {
		return ErrNilApplication
	}
	state := c.sharedState()
	state.ownerLock.Lock()
	defer state.ownerLock.Unlock()
	if state.owner != nil && state.owner != app {
		return ErrContainerOwnershipConflict
	}
	state.owner = app
	return nil
}

func mustContainerMutation(err error) {
	if err != nil {
		panic(err)
	}
}
