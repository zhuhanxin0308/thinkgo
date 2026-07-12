package view

import "io"

// Driver 视图模板驱动接口
type Driver interface {
	// Config 配置驱动
	Config(config map[string]interface{}) error

	// Fetch 渲染模板并返回内容
	Fetch(template string, data map[string]interface{}) (string, error)

	// Display 渲染模板并写入 writer
	Display(w io.Writer, template string, data map[string]interface{}) error

	// Exists 检查模板是否存在
	Exists(template string) (bool, error)

	// SetFuncMap 设置模板函数映射
	SetFuncMap(funcMap map[string]interface{}) error
}
