package framework

import (
	"strings"
	"thinkgo/framework/context"
	"thinkgo/framework/validate"
)

// ControllerMiddleware 定义控制器级中间件配置项。
// 对应 ThinkPHP 的控制器 $middleware 属性。
type ControllerMiddleware struct {
	Name   string   // 中间件名称（别名）
	Only   []string // 仅对指定动作生效（为空表示全部生效）
	Except []string // 排除指定动作
}

// Controller 基础控制器
// 对应 ThinkPHP 的 BaseController
type Controller struct {
	App        *App
	Request    *context.Request
	viewData   map[string]interface{}
	middleware []ControllerMiddleware // 控制器级中间件声明
}

// GetMiddleware 返回控制器声明的中间件列表。
// HTTP 内核在分发时会读取此列表，并将匹配的中间件应用到请求处理链中。
func (c *Controller) GetMiddleware() []ControllerMiddleware {
	return c.middleware
}

// SetMiddleware 设置控制器级中间件。
// 对应 ThinkPHP 控制器中的 $middleware 属性声明。
// 子控制器可在构造时调用此方法声明所需的中间件。
func (c *Controller) SetMiddleware(middlewares ...ControllerMiddleware) {
	c.middleware = middlewares
}

// Init 初始化控制器
func (c *Controller) Init(app *App, req *context.Request) {
	c.App = app
	c.Request = req
	c.viewData = make(map[string]interface{})
}

// Assign 为视图赋值变量
func (c *Controller) Assign(key string, value interface{}) {
	c.viewData[key] = value
}

// View 渲染模板
func (c *Controller) View(name string, data ...map[string]interface{}) string {
	// 合并已赋值数据与传入数据
	d := make(map[string]interface{})
	for k, v := range c.viewData {
		d[k] = v
	}
	if len(data) > 0 {
		for k, v := range data[0] {
			d[k] = v
		}
	}

	var b strings.Builder
	err := c.App.View.Render(&b, name, d)
	if err != nil {
		// 记录详细错误到日志，不将模板路径等敏感信息暴露给用户
		c.App.Log.Error("模板渲染失败 [" + name + "]: " + err.Error())
		return "页面渲染出错，请稍后再试"
	}
	return b.String()
}

// Fetch View 的别名
func (c *Controller) Fetch(name string, data ...map[string]interface{}) string {
	return c.View(name, data...)
}

// Success 返回标准成功 JSON 响应
func (c *Controller) Success(data interface{}, msg ...string) *context.Response {
	message := "success"
	if len(msg) > 0 {
		message = msg[0]
	}
	return c.Result(data, 0, message)
}

// Error 返回标准错误 JSON 响应
func (c *Controller) Error(msg string, code ...int) *context.Response {
	errorCode := 1
	if len(code) > 0 {
		errorCode = code[0]
	}
	return c.Result(nil, errorCode, msg)
}

// Result 返回通用 JSON 响应
func (c *Controller) Result(data interface{}, code int, msg string) *context.Response {
	return context.NewResponse().Json(map[string]interface{}{
		"code": code,
		"msg":  msg,
		"data": data,
	})
}

// Redirect 返回重定向响应
func (c *Controller) Redirect(url string, code ...int) *context.Response {
	return context.NewResponse().Redirect(url, code...)
}

// Validate 验证请求数据，数据违规与规则配置错误分别通过 Result 和 error 返回。
func (c *Controller) Validate(data map[string]interface{}, rules map[string]string) (validate.Result, error) {
	return validate.NewValidator().SetRules(rules).Validate(data)
}

// Lang 获取多语言翻译（便捷方法）
// 对应 ThinkPHP 的 lang('key')
// 从请求上下文读取当前语言（并发安全），而非依赖全局状态
func (c *Controller) Lang(key string, vars ...map[string]interface{}) string {
	var v map[string]interface{}
	if len(vars) > 0 {
		v = vars[0]
	}
	// 从请求上下文获取当前语言
	lang := ""
	if c.Request != nil {
		if l, ok := c.Request.GetData(LangRequestKey).(string); ok {
			lang = l
		}
	}
	return c.App.Lang.Get(key, v, lang)
}

// GetLang 获取当前请求的语言标识
// 供控制器创建服务时传递给服务层，确保服务内部多语言翻译使用正确的请求语言
func (c *Controller) GetLang() string {
	if c.Request != nil {
		if l, ok := c.Request.GetData(LangRequestKey).(string); ok {
			return l
		}
	}
	return ""
}
