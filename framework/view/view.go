package view

import (
	"io"
	"sync"
	"thinkgo/framework/debug"
)

// View 视图管理器，负责模板渲染
type View struct {
	debug  *debug.Debug
	config map[string]interface{}
	driver Driver
	data   map[string]interface{} // 全局模板变量
	mutex  sync.RWMutex
}

// NewView 创建视图管理器
func NewView(debug *debug.Debug, config map[string]interface{}) *View {
	v := &View{
		debug:  debug,
		config: config,
		data:   make(map[string]interface{}),
	}
	return v
}

// SetDriver 设置视图驱动
func (v *View) SetDriver(driver Driver) {
	v.driver = driver
	v.driver.Config(v.config)
}

// SetFuncMap 设置模板函数映射
func (v *View) SetFuncMap(funcMap map[string]interface{}) {
	v.driver.SetFuncMap(funcMap)
}

// Assign 设置全局模板变量
func (v *View) Assign(key string, value interface{}) {
	v.mutex.Lock()
	defer v.mutex.Unlock()
	v.data[key] = value
}

// Get 获取全局模板变量
func (v *View) Get(key string) interface{} {
	v.mutex.RLock()
	defer v.mutex.RUnlock()
	return v.data[key]
}

// Render 渲染模板并写入 writer
func (v *View) Render(w io.Writer, name string, data map[string]interface{}) error {
	if v.debug != nil {
		v.debug.AddFile(name)
	}

	// 合并全局变量与局部变量（局部变量优先）
	v.mutex.RLock()
	mergedData := make(map[string]interface{})
	for k, val := range v.data {
		mergedData[k] = val
	}
	v.mutex.RUnlock()

	for k, val := range data {
		mergedData[k] = val
	}

	return v.driver.Display(w, name, mergedData)
}

// Fetch 渲染模板并返回内容字符串
func (v *View) Fetch(name string, data map[string]interface{}) (string, error) {
	// 合并全局变量与局部变量
	v.mutex.RLock()
	mergedData := make(map[string]interface{})
	for k, val := range v.data {
		mergedData[k] = val
	}
	v.mutex.RUnlock()

	for k, val := range data {
		mergedData[k] = val
	}

	return v.driver.Fetch(name, mergedData)
}

// Exists 检查模板是否存在
func (v *View) Exists(name string) bool {
	return v.driver.Exists(name)
}
