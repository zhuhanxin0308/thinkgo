package framework

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"

	frameworkVersion "github.com/zhuhanxin0308/thinkgo/v3/version"
)

// Debug 设置应用调试模式并返回当前 App，对应 ThinkPHP App::debug。
// 不传参数时默认开启调试模式。
func (app *App) Debug(debug ...bool) *App {
	if app == nil {
		return nil
	}
	enabled := true
	if len(debug) > 1 {
		panic(fmt.Errorf("Debug 最多只能指定一个参数"))
	}
	if len(debug) == 1 {
		enabled = debug[0]
	}
	app.metadataMu.Lock()
	app.debugOverrideSet = true
	app.debugOverride = enabled
	app.DebugMode = enabled
	app.metadataMu.Unlock()
	return app
}

// SetNamespace 设置应用业务命名空间并返回当前 App。
func (app *App) SetNamespace(namespace string) *App {
	if app == nil {
		return nil
	}
	app.metadataMu.Lock()
	app.namespace = namespace
	app.metadataMu.Unlock()
	return app
}

// GetNamespace 返回当前应用业务命名空间，默认值为 app。
func (app *App) GetNamespace() string {
	if app == nil {
		return ""
	}
	app.metadataMu.RLock()
	namespace := app.namespace
	app.metadataMu.RUnlock()
	if namespace == "" {
		return "app"
	}
	return namespace
}

// SetBaseEnvName 设置公共环境文件标识并返回当前 App。
func (app *App) SetBaseEnvName(name string) *App {
	if app == nil {
		return nil
	}
	app.metadataMu.Lock()
	app.baseEnvName = strings.TrimSpace(name)
	app.metadataMu.Unlock()
	return app
}

// SetEnvName 设置当前环境文件标识并返回当前 App。
func (app *App) SetEnvName(name string) *App {
	if app == nil {
		return nil
	}
	app.storeEnvName(strings.TrimSpace(name))
	return app
}

func (app *App) storeEnvName(name string) {
	if app == nil {
		return
	}
	app.metadataMu.Lock()
	app.envName = name
	app.metadataMu.Unlock()
}

func (app *App) environmentNames() (string, string) {
	if app == nil {
		return "", ""
	}
	app.metadataMu.RLock()
	defer app.metadataMu.RUnlock()
	return app.baseEnvName, app.envName
}

// Version 返回框架语义版本号。
func (app *App) Version() string {
	return frameworkVersion.Number
}

// GetThinkPath 返回宿主项目中的框架目录。
func (app *App) GetThinkPath() string {
	if app == nil {
		return ""
	}
	return filepath.Join(app.GetRootPath(), "framework")
}

// GetConfigExt 返回当前配置文件扩展名。Go 版本使用 JSON 配置。
func (app *App) GetConfigExt() string {
	if app == nil {
		return ""
	}
	app.metadataMu.RLock()
	extension := app.configExt
	app.metadataMu.RUnlock()
	if extension == "" {
		return ".json"
	}
	return extension
}

// GetBeginTime 返回应用初始化开始时的 Unix 秒时间戳。
func (app *App) GetBeginTime() float64 {
	if app == nil {
		return 0
	}
	return math.Float64frombits(app.beginTime.Load())
}

// GetBeginMem 返回应用初始化开始时 Go 运行时已分配的堆内存字节数。
func (app *App) GetBeginMem() uint64 {
	if app == nil {
		return 0
	}
	return app.beginMem.Load()
}

func (app *App) captureInitializationStart() {
	if app == nil {
		return
	}
	app.beginTime.Store(math.Float64bits(float64(app.Now().UnixNano()) / 1e9))
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	app.beginMem.Store(memory.Alloc)
}

// LoadEnv 加载 .env 或 .env.<name> 环境文件。
func (app *App) LoadEnv(envName ...string) error {
	if app == nil || app.env == nil {
		return ErrNilApplication
	}
	if len(envName) > 1 {
		return fmt.Errorf("环境标识最多只能指定一次")
	}
	name := ""
	if len(envName) == 1 {
		name = strings.TrimSpace(envName[0])
	}
	if !validEnvironmentSuffix(name) {
		return fmt.Errorf("环境标识 %q 非法", name)
	}
	filename := ".env"
	if name != "" {
		filename += "." + name
	}
	return app.env.Load(filepath.Join(app.GetRootPath(), filename))
}

func validEnvironmentSuffix(name string) bool {
	if name == "" {
		return true
	}
	for _, character := range name {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func (app *App) resolveConfigExtension() error {
	if app == nil || app.env == nil {
		return ErrNilApplication
	}
	extension := strings.TrimSpace(app.env.Get("config_ext", ".json"))
	if extension == "json" {
		extension = ".json"
	}
	if extension != ".json" {
		return fmt.Errorf("当前框架仅支持 .json 配置扩展名，实际为 %q", extension)
	}
	app.metadataMu.Lock()
	app.configExt = extension
	app.metadataMu.Unlock()
	return nil
}

// LoadConfig 优先加载 runtime/config.json 配置缓存；缓存不存在时再扫描
// config 目录，与 ThinkPHP App::load 的配置缓存优先级保持一致。
func (app *App) LoadConfig() error {
	if app == nil || app.config == nil {
		return ErrNilApplication
	}
	cachePath := filepath.Join(app.GetRuntimePath(), "config.json")
	if information, err := os.Lstat(cachePath); err == nil {
		if information.Mode()&os.ModeSymlink != 0 || !information.Mode().IsRegular() {
			return fmt.Errorf("配置缓存必须是普通文件: %s", cachePath)
		}
		return app.config.Load(cachePath, "")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("检查配置缓存失败: %w", err)
	}
	return app.config.LoadAll(app.GetConfigPath())
}

// LoadLangPack 切换到应用默认语言包，对应 ThinkPHP App::loadLangPack。
func (app *App) LoadLangPack() {
	if app == nil || app.lang == nil {
		return
	}
	defaultLanguage := app.lang.GetDefaultLang()
	if app.lang.HasLang(defaultLanguage) {
		_ = app.lang.SetLang(defaultLanguage)
	}
}

// Boot 启动全部已注册应用服务。
func (app *App) Boot() error {
	return app.BootProviders()
}

// ParseClass 按 ThinkPHP 规则解析业务层类名。
func (app *App) ParseClass(layer, name string) string {
	normalized := strings.NewReplacer("/", `\`, ".", `\`).Replace(name)
	parts := strings.Split(normalized, `\`)
	for len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	class := ""
	if len(parts) > 0 {
		class = studlyClassName(parts[len(parts)-1])
		parts = parts[:len(parts)-1]
	}
	result := []string{app.GetNamespace(), layer}
	result = append(result, parts...)
	result = append(result, class)
	return strings.Join(result, `\`)
}

func studlyClassName(value string) string {
	words := strings.FieldsFunc(value, func(character rune) bool {
		return character == '_' || character == '-' || unicode.IsSpace(character)
	})
	for index, word := range words {
		runes := []rune(strings.ToLower(word))
		if len(runes) > 0 {
			runes[0] = unicode.ToUpper(runes[0])
		}
		words[index] = string(runes)
	}
	return strings.Join(words, "")
}

// RunningInConsole 判断应用是否由控制台入口构造。
func (app *App) RunningInConsole() bool {
	if app == nil {
		return false
	}
	app.metadataMu.RLock()
	consoleMode := app.consoleMode
	app.metadataMu.RUnlock()
	return consoleMode
}
