package framework

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

const modelServicePrefix = "model."

// ResolveModel 从当前应用、请求或任务作用域创建一个独立且已初始化的业务模型。
// T 必须是已注册模型的结构体指针；事务等选项只作用于本次构造。
func ResolveModel[T any](resolver TypedServiceResolver, options ...db.ModelOption) (T, error) {
	var zero T
	target := reflect.TypeOf((*T)(nil)).Elem()
	instance, err := ResolveModelType(resolver, target, options...)
	if err != nil {
		return zero, err
	}
	model, ok := instance.(T)
	if !ok {
		return zero, fmt.Errorf("%w: 模型期望 %s，实际为 %T", ErrServiceTypeMismatch, target, instance)
	}
	return model, nil
}

// ResolveModelType 是供动作依赖注入使用的精确类型解析入口。
// 完整包路径参与服务键，避免不同包中同名类型被错误匹配。
func ResolveModelType(resolver TypedServiceResolver, target reflect.Type, options ...db.ModelOption) (interface{}, error) {
	key, err := modelTypeServiceKey(target)
	if err != nil {
		return nil, err
	}
	if isNilServiceInstance(resolver) {
		return nil, fmt.Errorf("%w: 模型 %s 缺少解析器", ErrServiceUnavailable, target)
	}
	params := make([]interface{}, len(options))
	for index, option := range options {
		params[index] = option
	}
	instance, err := resolver.Make(key, params...)
	if err != nil {
		return nil, err
	}
	if isNilServiceInstance(instance) || !reflect.TypeOf(instance).AssignableTo(target) {
		return nil, fmt.Errorf("%w: 模型期望 %s，实际为 %T", ErrServiceTypeMismatch, target, instance)
	}
	return instance, nil
}

// HasModelType 判断当前应用是否已经装配了指定业务模型类型。
func (app *App) HasModelType(target reflect.Type) bool {
	if app == nil {
		return false
	}
	app.registry.lock.RLock()
	key, exists := app.registry.modelBindings[target]
	app.registry.lock.RUnlock()
	return exists && app.Has(key)
}

func modelTypeServiceKey(target reflect.Type) (string, error) {
	if target == nil || target.Kind() != reflect.Pointer || target.Elem().Kind() != reflect.Struct || target.Elem().Name() == "" {
		return "", fmt.Errorf("%w: 模型解析需要具名结构体指针，实际为 %v", db.ErrInvalidModel, target)
	}
	if err := db.ValidateModelType(target); err != nil {
		return "", err
	}
	return modelServicePrefix + "type[" + target.Elem().PkgPath() + "]." + target.Elem().Name(), nil
}

// initializeModelBindings 在目标应用进程校验并预热模型元数据，只为 ORM 模型建立瞬时工厂。
func (app *App) initializeModelBindings() error {
	models := app.registry.snapshotModels()
	bindings := make(map[string]reflect.Type, len(models)*2)
	typeBindings := make(map[reflect.Type]string, len(models))
	for name, modelType := range models {
		if err := db.PrewarmModelMetadata(modelType); err != nil {
			return fmt.Errorf("模型 %q 元数据无效: %w", name, err)
		}
		if !embedsModelBase(modelType, make(map[reflect.Type]bool)) {
			continue
		}
		if err := db.ValidateModelType(modelType); err != nil {
			return fmt.Errorf("模型 %q 结构无效: %w", name, err)
		}
		typedKey, err := modelTypeServiceKey(reflect.PointerTo(modelType))
		if err != nil {
			return err
		}
		for _, key := range []string{modelServicePrefix + name, typedKey} {
			if previous, exists := bindings[key]; exists && previous != modelType {
				return fmt.Errorf("%w: 模型服务 %q 同时指向 %s 与 %s", ErrDuplicateRegistration, key, previous, modelType)
			}
			bindings[key] = modelType
		}
		typeBindings[reflect.PointerTo(modelType)] = typedKey
	}
	keys := make([]string, 0, len(bindings))
	for key := range bindings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return app.publishModelBindings(keys, bindings, typeBindings)
}

// publishModelBindings 在同一变更门禁和容器锁内先校验再发布，避免并发变更留下部分模型工厂。
func (app *App) publishModelBindings(keys []string, bindings map[string]reflect.Type, typeBindings map[reflect.Type]string) error {
	container, err := app.ownedContainer()
	if err != nil {
		return err
	}
	app.serviceMutationMu.Lock()
	defer app.serviceMutationMu.Unlock()
	if err := app.containerMutationTargetError(container); err != nil {
		return err
	}
	state := container.sharedState()
	state.lock.Lock()
	defer state.lock.Unlock()
	for _, key := range keys {
		if _, exists := state.bindings[key]; exists {
			return fmt.Errorf("%w: 模型服务 %q 已有显式绑定", ErrDuplicateRegistration, key)
		}
		if _, exists := state.instances[key]; exists {
			return fmt.Errorf("%w: 模型服务 %q 已有显式绑定", ErrDuplicateRegistration, key)
		}
	}
	for _, key := range keys {
		factory := automaticModelFactory(bindings[key])
		state.bindings[key] = binding{
			concrete:   factory,
			factory:    compileFactoryPlan(factory),
			lifecycle:  Factory,
			generation: state.nextGenerationLocked(key),
		}
	}
	app.registry.lock.Lock()
	app.registry.modelBindings = typeBindings
	app.registry.lock.Unlock()
	return nil
}

func automaticModelFactory(modelType reflect.Type) func(*Container, ...db.ModelOption) (interface{}, error) {
	return func(container *Container, options ...db.ModelOption) (interface{}, error) {
		if container.currentResolution().containsLifecycle(Singleton) {
			return nil, fmt.Errorf("%w: 模型 %s 必须按操作创建", ErrScopedDependencyCaptured, modelType)
		}
		service, err := container.Make(string(ServiceDB))
		if err != nil {
			return nil, fmt.Errorf("%w: 解析模型 %s 的数据库失败: %w", ErrServiceUnavailable, modelType, err)
		}
		database, ok := service.(*db.DB)
		if !ok || database == nil {
			return nil, fmt.Errorf("%w: 模型 %s 的数据库类型为 %T", ErrServiceTypeMismatch, modelType, service)
		}
		instance := reflect.New(modelType).Interface()
		if _, err := db.NewModelFor(container.Context(), database, instance, options...); err != nil {
			return nil, fmt.Errorf("构造模型 %s 失败: %w", modelType, err)
		}
		return instance, nil
	}
}

func embedsModelBase(target reflect.Type, visiting map[reflect.Type]bool) bool {
	if target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	if target.Kind() != reflect.Struct || visiting[target] {
		return false
	}
	visiting[target] = true
	defer delete(visiting, target)
	base := reflect.TypeOf((*db.Model)(nil))
	for index := 0; index < target.NumField(); index++ {
		field := target.Field(index)
		if !field.Anonymous {
			continue
		}
		// 值嵌入也属于需要校验的模型声明，由 db 拒绝复制内部锁的错误用法。
		if field.Type == base || field.Type == base.Elem() || embedsModelBase(field.Type, visiting) {
			return true
		}
	}
	return false
}
