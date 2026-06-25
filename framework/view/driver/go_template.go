package driver

import (
	"bytes"
	"fmt"
	"html/template"
	"io"
	"os"
	"path"
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
	path, ok := d.safeTemplatePath(tmplName)
	if !ok {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// SetFuncMap 设置模板函数映射
func (d *GoTemplate) SetFuncMap(funcMap map[string]interface{}) {
	d.config["func_map"] = template.FuncMap(funcMap)
}

// getTemplate 从缓存获取或解析模板
func (d *GoTemplate) getTemplate(tmplName string) (*template.Template, error) {
	path, ok := d.safeTemplatePath(tmplName)
	if !ok {
		return nil, fmt.Errorf("非法模板名: %s", tmplName)
	}

	// 检查缓存
	d.mutex.RLock()
	if tmpl, ok := d.cache[path]; ok {
		d.mutex.RUnlock()
		return tmpl, nil
	}
	d.mutex.RUnlock()

	// 解析模板
	if _, err := os.Stat(path); err != nil {
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

// safeTemplatePath 解析模板完整路径，并阻断 ".." 越权与绝对路径逃逸，
// 防止把用户可控的模板名变成任意文件读取（LFI）。
// 返回 (绝对路径, 是否合法)。
func (d *GoTemplate) safeTemplatePath(tmplName string) (string, bool) {
	viewPath, _ := d.config["view_path"].(string)
	viewSuffix, _ := d.config["view_suffix"].(string)
	if viewSuffix == "" {
		viewSuffix = "html"
	}

	// 统一分隔符并清理路径，去除 ".." 等穿越片段。
	tmplName = strings.ReplaceAll(tmplName, "\\", "/")
	if !strings.HasSuffix(tmplName, "."+viewSuffix) {
		tmplName += "." + viewSuffix
	}
	cleanRel := strings.TrimPrefix(path.Clean("/"+tmplName), "/")

	target := filepath.Join(viewPath, filepath.FromSlash(cleanRel))

	// 未配置 view_path 时无基准目录可校验，按清理后的相对路径返回。
	if strings.TrimSpace(viewPath) == "" {
		return target, true
	}

	baseAbs, err := filepath.Abs(viewPath)
	if err != nil {
		return "", false
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return "", false
	}
	// 解析后的路径必须仍位于视图目录内。
	if targetAbs != baseAbs && !strings.HasPrefix(targetAbs, baseAbs+string(os.PathSeparator)) {
		return "", false
	}
	return targetAbs, true
}
