package view

import (
	"errors"
	"io"
	"reflect"
	"sync"

	"github.com/zhuhanxin0308/thinkgo/v3/debug"
)

var (
	// ErrViewDriverNotConfigured 表示视图尚未安装可用驱动。
	ErrViewDriverNotConfigured = errors.New("视图驱动未配置")
	// ErrInvalidViewWriter 表示 Render 没有收到可用写入器。
	ErrInvalidViewWriter = errors.New("视图写入器不能为空")
	// ErrRequestFuncMapUnsupported 表示驱动不能安全隔离单次渲染函数。
	ErrRequestFuncMapUnsupported = errors.New("视图驱动不支持请求级模板函数")
)

// RenderOptions 描述一次渲染私有的调试采集器和模板函数覆盖。
type RenderOptions struct {
	Collector *debug.Debug
	FuncMap   map[string]interface{}
}

// View 视图管理器，负责模板渲染
type View struct {
	debug  *debug.Debug
	config map[string]interface{}
	driver Driver
	data   map[string]interface{} // 全局模板变量
	dataMu sync.RWMutex
	drvMu  sync.RWMutex
}

// NewView 创建视图管理器；debug 仅供旧 Render/Fetch 兼容入口使用。
func NewView(debug *debug.Debug, config map[string]interface{}) *View {
	v := &View{
		debug:  debug,
		config: cloneViewMap(config),
		data:   make(map[string]interface{}),
	}
	return v
}

// SetDriver 设置视图驱动
func (v *View) SetDriver(driver Driver) error {
	if v == nil || isNilViewDriver(driver) {
		return ErrViewDriverNotConfigured
	}
	if err := driver.Config(cloneViewMap(v.config)); err != nil {
		return err
	}
	v.drvMu.Lock()
	v.driver = driver
	v.drvMu.Unlock()
	return nil
}

// SetFuncMap 设置模板函数映射
func (v *View) SetFuncMap(funcMap map[string]interface{}) error {
	if v == nil {
		return ErrViewDriverNotConfigured
	}
	v.drvMu.RLock()
	driver := v.driver
	if isNilViewDriver(driver) {
		v.drvMu.RUnlock()
		return ErrViewDriverNotConfigured
	}
	err := driver.SetFuncMap(cloneViewMap(funcMap))
	v.drvMu.RUnlock()
	return err
}

// Assign 设置全局模板变量
func (v *View) Assign(key string, value interface{}) {
	if v == nil {
		return
	}
	v.dataMu.Lock()
	defer v.dataMu.Unlock()
	if v.data == nil {
		v.data = make(map[string]interface{})
	}
	v.data[key] = value
}

// Get 获取全局模板变量
func (v *View) Get(key string) interface{} {
	if v == nil {
		return nil
	}
	v.dataMu.RLock()
	defer v.dataMu.RUnlock()
	return v.data[key]
}

// Render 渲染模板并写入 writer
func (v *View) Render(w io.Writer, name string, data map[string]interface{}) error {
	var collector *debug.Debug
	if v != nil {
		collector = v.debug
	}
	return v.RenderWithDebug(collector, w, name, data)
}

// RenderWithDebug 使用显式请求 collector 渲染模板并写入 writer。
// collector 只接收本次成功渲染的模板记录，不会写回 View 的构造时配置。
func (v *View) RenderWithDebug(collector *debug.Debug, w io.Writer, name string, data map[string]interface{}) error {
	return v.RenderWithOptions(RenderOptions{Collector: collector}, w, name, data)
}

// RenderWithOptions 使用请求私有选项渲染，不修改 View 或驱动的共享配置。
func (v *View) RenderWithOptions(options RenderOptions, w io.Writer, name string, data map[string]interface{}) error {
	if isNilViewWriter(w) {
		return ErrInvalidViewWriter
	}
	driver, err := v.currentDriver()
	if err != nil {
		return err
	}
	merged := v.mergeData(data)
	if len(options.FuncMap) == 0 {
		err = driver.Display(w, name, merged)
	} else if scoped, ok := driver.(RequestFuncDriver); ok && !isNilRequestFuncDriver(scoped) {
		err = scoped.DisplayWithFuncMap(w, name, merged, cloneViewMap(options.FuncMap))
	} else {
		err = ErrRequestFuncMapUnsupported
	}
	if err != nil {
		return err
	}
	if options.Collector != nil {
		options.Collector.AddFile(name)
	}
	return nil
}

// Fetch 渲染模板并返回内容字符串
func (v *View) Fetch(name string, data map[string]interface{}) (string, error) {
	var collector *debug.Debug
	if v != nil {
		collector = v.debug
	}
	return v.FetchWithDebug(collector, name, data)
}

// FetchWithDebug 使用显式请求 collector 渲染模板并返回内容字符串。
// collector 只接收本次成功渲染的模板记录，不会改变后续请求的采集目标。
func (v *View) FetchWithDebug(collector *debug.Debug, name string, data map[string]interface{}) (string, error) {
	return v.FetchWithOptions(RenderOptions{Collector: collector}, name, data)
}

// FetchWithOptions 使用请求私有选项渲染为字符串，不污染后续请求。
func (v *View) FetchWithOptions(options RenderOptions, name string, data map[string]interface{}) (string, error) {
	driver, err := v.currentDriver()
	if err != nil {
		return "", err
	}
	merged := v.mergeData(data)
	content := ""
	if len(options.FuncMap) == 0 {
		content, err = driver.Fetch(name, merged)
	} else if scoped, ok := driver.(RequestFuncDriver); ok && !isNilRequestFuncDriver(scoped) {
		content, err = scoped.FetchWithFuncMap(name, merged, cloneViewMap(options.FuncMap))
	} else {
		err = ErrRequestFuncMapUnsupported
	}
	if err != nil {
		return "", err
	}
	if options.Collector != nil {
		options.Collector.AddFile(name)
	}
	return content, nil
}

// Exists 检查模板是否存在
func (v *View) Exists(name string) (bool, error) {
	driver, err := v.currentDriver()
	if err != nil {
		return false, err
	}
	return driver.Exists(name)
}

func (v *View) currentDriver() (Driver, error) {
	if v == nil {
		return nil, ErrViewDriverNotConfigured
	}
	v.drvMu.RLock()
	driver := v.driver
	v.drvMu.RUnlock()
	if isNilViewDriver(driver) {
		return nil, ErrViewDriverNotConfigured
	}
	return driver, nil
}

func (v *View) mergeData(local map[string]interface{}) map[string]interface{} {
	v.dataMu.RLock()
	merged := make(map[string]interface{}, len(v.data)+len(local))
	for key, value := range v.data {
		merged[key] = value
	}
	v.dataMu.RUnlock()
	for key, value := range local {
		merged[key] = value
	}
	return merged
}

func cloneViewMap(source map[string]interface{}) map[string]interface{} {
	cloned := make(map[string]interface{}, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func isNilViewDriver(driver Driver) bool {
	if driver == nil {
		return true
	}
	value := reflect.ValueOf(driver)
	return value.Kind() == reflect.Pointer && value.IsNil()
}

func isNilRequestFuncDriver(driver RequestFuncDriver) bool {
	if driver == nil {
		return true
	}
	value := reflect.ValueOf(driver)
	return value.Kind() == reflect.Pointer && value.IsNil()
}

func isNilViewWriter(writer io.Writer) bool {
	if writer == nil {
		return true
	}
	value := reflect.ValueOf(writer)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
