package driver

import (
	"bytes"
	"fmt"
	"html/template"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// GoTemplate 基于 html/template 的视图驱动
type GoTemplate struct {
	config map[string]interface{}
	cache  map[string]*template.Template
	mutex  sync.RWMutex
}

// NewGoTemplate 创建 GoTemplate 驱动
func NewGoTemplate() *GoTemplate {
	return &GoTemplate{
		config: make(map[string]interface{}),
		cache:  make(map[string]*template.Template),
	}
}

// Config 配置驱动参数
func (d *GoTemplate) Config(config map[string]interface{}) {
	d.config = config
	if _, ok := d.config["view_path"]; !ok {
		d.config["view_path"] = ""
	}
	if _, ok := d.config["view_suffix"]; !ok {
		d.config["view_suffix"] = "html"
	}
	if _, ok := d.config["view_depr"]; !ok {
		d.config["view_depr"] = "/"
	}
}

// Fetch 渲染模板并返回内容
func (d *GoTemplate) Fetch(tmplName string, data map[string]interface{}) (string, error) {
	var buf bytes.Buffer
	err := d.Display(&buf, tmplName, data)
	if err != nil {
		return "", err
	}
	return buf.String(), nil
}

// Display 渲染模板并写入 writer
func (d *GoTemplate) Display(w io.Writer, tmplName string, data map[string]interface{}) error {
	tmpl, err := d.getTemplate(tmplName)
	if err != nil {
		return err
	}
	return tmpl.Execute(w, data)
}

// Exists 检查模板文件是否存在
func (d *GoTemplate) Exists(tmplName string) bool {
	path := d.getTemplatePath(tmplName)
	_, err := os.Stat(path)
	return err == nil
}

// SetFuncMap 设置模板函数映射
func (d *GoTemplate) SetFuncMap(funcMap map[string]interface{}) {
	d.config["func_map"] = template.FuncMap(funcMap)
}

// getTemplate 从缓存获取或解析模板
func (d *GoTemplate) getTemplate(tmplName string) (*template.Template, error) {
	path := d.getTemplatePath(tmplName)

	// 检查缓存
	d.mutex.RLock()
	if tmpl, ok := d.cache[path]; ok {
		d.mutex.RUnlock()
		return tmpl, nil
	}
	d.mutex.RUnlock()

	// 解析模板
	if !d.Exists(tmplName) {
		return nil, fmt.Errorf("模板文件不存在: %s", path)
	}

	// 创建模板实例并注入自定义函数
	tmpl := template.New(filepath.Base(path))

	if funcMap, ok := d.config["func_map"].(template.FuncMap); ok {
		tmpl.Funcs(funcMap)
	}

	tmpl, err := tmpl.ParseFiles(path)
	if err != nil {
		return nil, err
	}

	// 写入缓存
	d.mutex.Lock()
	d.cache[path] = tmpl
	d.mutex.Unlock()

	return tmpl, nil
}

// getTemplatePath 解析模板完整路径
func (d *GoTemplate) getTemplatePath(tmplName string) string {
	viewPath := d.config["view_path"].(string)
	viewSuffix := d.config["view_suffix"].(string)

	// 如果模板名不包含后缀则自动追加
	if !strings.HasSuffix(tmplName, "."+viewSuffix) {
		tmplName += "." + viewSuffix
	}

	return filepath.Join(viewPath, tmplName)
}
