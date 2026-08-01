package view

import (
	"errors"
	"io"
	"reflect"
	"sync"

	"thinkgo/framework/debug"
)

var (
	// ErrViewDriverNotConfigured 表示视图尚未安装可用驱动。
	ErrViewDriverNotConfigured = errors.New("视图驱动未配置")
	// ErrInvalidViewWriter 表示 Render 没有收到可用写入器。
	ErrInvalidViewWriter = errors.New("视图写入器不能为空")
)

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
	if isNilViewWriter(w) {
		return ErrInvalidViewWriter
	}
	driver, err := v.currentDriver()
	if err != nil {
		return err
	}
	if err = driver.Display(w, name, v.mergeData(data)); err != nil {
		return err
	}
	if collector != nil {
		collector.AddFile(name)
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
	driver, err := v.currentDriver()
	if err != nil {
		return "", err
	}
	content, err := driver.Fetch(name, v.mergeData(data))
	if err != nil {
		return "", err
	}
	if collector != nil {
		collector.AddFile(name)
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
