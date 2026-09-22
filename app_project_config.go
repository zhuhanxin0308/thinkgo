package framework

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/zhuhanxin0308/thinkgo/v3/config"
	"github.com/zhuhanxin0308/thinkgo/v3/env"
)

// ErrProjectConfigurationOverrideUnavailable 表示项目配置尚未初始化完成，
// 或已经进入不允许宿主参数覆盖的生命周期阶段。
var ErrProjectConfigurationOverrideUnavailable = errors.New("项目配置运行时覆盖不可用")

// projectConfigurationState 保存一个项目只解析一次的环境与配置基线。
// 基线本身不交给业务应用修改，各应用初始化时只取得递归深拷贝。
type projectConfigurationState struct {
	once        sync.Once
	owner       *App
	config      *config.Config
	environment *env.Env
	baseEnvName string
	envName     string
	configExt   string
	err         error
}

func newProjectConfigurationState(owner *App) *projectConfigurationState {
	return &projectConfigurationState{owner: owner}
}

// ensureProjectConfigurationState 兼容手工构造的历史 App 值，并保证原生
// 多应用无论在根应用初始化前后构建，都共享同一个项目解析状态。
func (app *App) ensureProjectConfigurationState() *projectConfigurationState {
	if app == nil {
		return nil
	}
	app.metadataMu.Lock()
	if app.projectConfiguration == nil {
		app.projectConfiguration = newProjectConfigurationState(app)
	}
	state := app.projectConfiguration
	app.metadataMu.Unlock()
	return state
}

// prepareProjectConfiguration 等待项目基线解析完成，并把独立工作副本安装
// 到当前应用已有的 Config、Env 服务中，保持公开服务指针的兼容性。
func (app *App) prepareProjectConfiguration() error {
	if app == nil {
		return ErrNilApplication
	}
	state := app.ensureProjectConfigurationState()
	if state == nil {
		return fmt.Errorf("项目配置状态不可用")
	}
	state.once.Do(state.load)
	if state.config != nil && app.config != nil {
		app.config.ReplaceWithSnapshot(state.config)
	}
	if state.environment != nil && app.env != nil {
		app.env.ReplaceWithSnapshot(state.environment)
	}
	app.metadataMu.Lock()
	app.baseEnvName = state.baseEnvName
	app.envName = state.envName
	app.configExt = state.configExt
	app.projectConfigSnapshot = state.config
	app.metadataMu.Unlock()
	return state.err
}

// load 始终使用项目入口 App 解析根目录环境、配置文件和配置缓存。
// 即使子应用先初始化，也不会改用子应用自己的运行时目录或重新读取外部状态。
func (state *projectConfigurationState) load() {
	if state == nil {
		return
	}
	if state.owner == nil {
		state.err = ErrNilApplication
		return
	}
	owner := state.owner
	// 子应用可以先于入口应用初始化；此时仍需冻结入口应用的配置、环境服务引用，
	// 避免并发 Instance 重绑定让一次项目解析混用两个服务实例。
	owner.serviceMutationMu.Lock()
	defer owner.serviceMutationMu.Unlock()
	if owner.config == nil || owner.env == nil {
		state.err = fmt.Errorf("项目配置基础服务未完成构造")
		return
	}

	baseEnvName, envName := owner.environmentNames()
	var loadErr error
	if baseEnvName != "" {
		if err := owner.LoadEnv(baseEnvName); err != nil {
			loadErr = errors.Join(loadErr, fmt.Errorf("load base environment failed: %w", err))
		}
	}
	if envName == "" {
		envName = owner.env.Get("env_name", "")
		owner.storeEnvName(envName)
	}
	if err := owner.LoadEnv(envName); err != nil {
		loadErr = errors.Join(loadErr, fmt.Errorf("load environment failed: %w", err))
	}
	if err := owner.resolveConfigExtension(); err != nil {
		loadErr = errors.Join(loadErr, err)
	}
	if err := owner.LoadConfig(); err != nil {
		loadErr = errors.Join(loadErr, fmt.Errorf("load config failed: %w", err))
	}
	if err := owner.config.ApplyEnvironment(owner.env); err != nil {
		loadErr = errors.Join(loadErr, fmt.Errorf("合并环境配置失败: %w", err))
	}

	// 先捕获环境，再冻结入口 Env；后续进程环境变化不会影响应用覆盖层。
	state.environment = owner.env.Snapshot()
	owner.env.ReplaceWithSnapshot(state.environment)
	state.config = owner.config.Clone()
	state.baseEnvName = baseEnvName
	state.envName = envName
	state.configExt = owner.GetConfigExt()
	state.err = loadErr
}

// ApplyProjectApplicationOverrides 在 HTTP 宿主构造前，把命令行等受信入口的
// app 配置同时写入当前应用、项目解析快照与后续子应用共享的配置基线。
// 所有目标先在独立副本中完成校验，任一覆盖非法时不会发布半成品配置。
func (app *App) ApplyProjectApplicationOverrides(overrides map[string]interface{}) error {
	if app == nil {
		return ErrNilApplication
	}
	if len(overrides) == 0 {
		return nil
	}

	app.lifecycle.transitionLock.Lock()
	defer app.lifecycle.transitionLock.Unlock()
	app.lifecycle.lock.Lock()
	state := app.lifecycle.state
	closed := app.lifecycle.closed
	app.lifecycle.lock.Unlock()
	if closed || state == ApplicationStateClosing || state == ApplicationStateClosed {
		return fmt.Errorf("%w: %w", ErrProjectConfigurationOverrideUnavailable, ErrApplicationClosed)
	}
	if state == ApplicationStateRunning {
		return fmt.Errorf("%w: %w", ErrProjectConfigurationOverrideUnavailable, ErrApplicationRunning)
	}
	if state == ApplicationStateFailed {
		return fmt.Errorf("%w: %w", ErrProjectConfigurationOverrideUnavailable, ErrApplicationFailed)
	}
	if state != ApplicationStateInitialized {
		return fmt.Errorf("%w: 当前应用状态为 %s", ErrProjectConfigurationOverrideUnavailable, state)
	}

	app.serviceMutationMu.Lock()
	defer app.serviceMutationMu.Unlock()
	app.metadataMu.RLock()
	projectSnapshot := app.projectConfigSnapshot
	projectState := app.projectConfiguration
	activeConfig := app.config
	app.metadataMu.RUnlock()
	if activeConfig == nil || projectSnapshot == nil || projectState == nil || projectState.config == nil {
		return fmt.Errorf("%w: 配置快照未完成初始化", ErrProjectConfigurationOverrideUnavailable)
	}

	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		if key == "" || strings.TrimSpace(key) != key || strings.HasPrefix(key, "app.") {
			return fmt.Errorf("%w: app 配置路径 %q 必须是规范的相对点路径", ErrProjectConfigurationOverrideUnavailable, key)
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)

	targets := make([]*config.Config, 0, 3)
	seen := make(map[*config.Config]struct{}, 3)
	for _, target := range []*config.Config{activeConfig, projectSnapshot, projectState.config} {
		if _, exists := seen[target]; exists {
			continue
		}
		seen[target] = struct{}{}
		targets = append(targets, target)
	}
	candidates := make([]*config.Config, len(targets))
	for index, target := range targets {
		candidate := target.Clone()
		for _, key := range keys {
			if err := candidate.Set("app."+key, overrides[key]); err != nil {
				return fmt.Errorf("%w: app.%s: %v", ErrProjectConfigurationOverrideUnavailable, key, err)
			}
		}
		candidates[index] = candidate
	}
	for index, target := range targets {
		target.ReplaceWithSnapshot(candidates[index])
	}
	return nil
}
