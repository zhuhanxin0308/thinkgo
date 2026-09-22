package framework

import (
	"fmt"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/v3/cache"
	"github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/debug"
	"github.com/zhuhanxin0308/thinkgo/v3/exception"
	"github.com/zhuhanxin0308/thinkgo/v3/log"
	"github.com/zhuhanxin0308/thinkgo/v3/validate"
	"github.com/zhuhanxin0308/thinkgo/v3/view"
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
	App           *App
	Request       *context.Request
	viewData      map[string]interface{}
	middleware    []ControllerMiddleware // 控制器级中间件声明
	batchValidate bool                   // 是否按 ThinkPHP BaseController 规则收集全部验证错误
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

// Initialize 是 ThinkPHP BaseController::initialize 对应的控制器初始化钩子。
// HTTP 分发器会在注入 App、Request 后、动作执行前调用具体控制器上的同名方法。
func (c *Controller) Initialize() {}

// SetBatchValidate 设置 BaseController 后续 Validate 调用是否收集全部错误。
func (c *Controller) SetBatchValidate(batch ...bool) *Controller {
	if c == nil {
		return c
	}
	enabled := true
	if len(batch) > 0 {
		enabled = batch[0]
	}
	c.batchValidate = enabled
	return c
}

// Assign 为视图赋值变量
func (c *Controller) Assign(key string, value interface{}) {
	if c.viewData == nil {
		c.viewData = make(map[string]interface{})
	}
	c.viewData[key] = value
}

// View 渲染模板，并把成功模板记录写入当前请求 collector。
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
	options := view.RenderOptions{Collector: debug.FromRequest(c.Request)}
	if c.App != nil && c.App.lang != nil {
		selectedLanguage := c.GetLang()
		options.FuncMap = map[string]interface{}{
			"lang": func(key string) string {
				return c.App.lang.Get(key, nil, selectedLanguage)
			},
		}
	}
	err := c.App.view.RenderWithOptions(options, &b, name, d)
	if err != nil {
		// 记录详细错误到日志，不将模板路径等敏感信息暴露给用户
		c.App.log.Error("模板渲染失败 [" + name + "]: " + log.SanitizeErrorText(err.Error()))
		return "页面渲染出错，请稍后再试"
	}
	return b.String()
}

// RequestCache 返回绑定当前请求 collector 的缓存 facade。
// facade 与应用根缓存共享驱动和生命周期状态，但调试数据仅属于当前请求。
func (c *Controller) RequestCache() *cache.Cache {
	if c == nil || c.App == nil || c.App.cache == nil {
		return nil
	}
	return c.App.cache.WithDebug(debug.FromRequest(c.Request))
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

// Validate 按 ThinkPHP BaseController::validate 的默认语义验证数据。
// 验证成功返回 true；数据不符合规则时 panic ValidateException，由 HTTP 异常生命周期统一处理。
func (c *Controller) Validate(data map[string]interface{}, definition interface{}, arguments ...interface{}) bool {
	validator, options, err := c.resolveControllerValidator(definition, arguments...)
	if err != nil {
		panic(err)
	}
	result, err := validator.Validate(data, options...)
	if err != nil {
		panic(err)
	}
	if result.Valid() {
		return true
	}
	violations := result.Violations()
	if len(violations) == 0 {
		panic(exception.NewValidateException("验证失败"))
	}
	if controllerValidationBatch(c, arguments) {
		errorsFound := make(map[string]string, len(violations))
		for _, violation := range violations {
			errorsFound[violation.Field] = violation.Message
		}
		panic(exception.NewBatchValidateException(errorsFound))
	}
	panic(exception.NewValidateException(violations[0].Message, violations[0].Field))
}

// ValidateResult 提供显式 Result/error 边界，供确实需要自行组织验证响应的业务使用。
func (c *Controller) ValidateResult(data map[string]interface{}, rules map[string]string) (validate.Result, error) {
	if c != nil && c.App != nil {
		return validate.ValidateRules(data, rules, validate.WithLocation(c.App.Location()))
	}
	return validate.ValidateRules(data, rules)
}

type controllerValidatorContract interface {
	Validate(map[string]interface{}, ...validate.Option) (validate.Result, error)
}

type controllerValidatorMessageSetter interface {
	SetMessages(map[string]string) *validate.Validator
}

func (c *Controller) resolveControllerValidator(definition interface{}, arguments ...interface{}) (controllerValidatorContract, []validate.Option, error) {
	options := make([]validate.Option, 0, 2)
	if c != nil && c.App != nil {
		options = append(options, validate.WithLocation(c.App.Location()))
	}
	var validator controllerValidatorContract
	switch typed := definition.(type) {
	case map[string]string:
		validator = validate.NewValidator().SetRules(typed)
	case controllerValidatorContract:
		validator = typed
	case string:
		resolved, scene, err := c.resolveNamedControllerValidator(typed)
		if err != nil {
			return nil, nil, err
		}
		validator = resolved
		if scene != "" {
			options = append(options, validate.WithScene(scene))
		}
	default:
		return nil, nil, fmt.Errorf("验证器定义必须是规则 map、验证器实例或验证器名称，实际为 %T", definition)
	}

	messages, _, err := controllerValidationArguments(arguments)
	if err != nil {
		return nil, nil, err
	}
	if len(messages) > 0 {
		setter, ok := validator.(controllerValidatorMessageSetter)
		if !ok {
			return nil, nil, fmt.Errorf("验证器 %T 不支持自定义消息", validator)
		}
		setter.SetMessages(messages)
	}
	if controllerValidationBatch(c, arguments) {
		options = append(options, validate.CollectAllErrors())
	}
	return validator, options, nil
}

func (c *Controller) resolveNamedControllerValidator(name string) (controllerValidatorContract, string, error) {
	if c == nil || c.App == nil {
		return nil, "", fmt.Errorf("按名称解析验证器需要可用的 App")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, "", fmt.Errorf("验证器名称不能为空")
	}
	validatorName, scene, _ := strings.Cut(name, ".")
	candidates := []string{c.App.ParseClass("validate", validatorName), validatorName}
	var resolveErr error
	for _, candidate := range candidates {
		instance, err := c.App.Make(candidate)
		if err != nil {
			resolveErr = err
			continue
		}
		validator, ok := instance.(controllerValidatorContract)
		if !ok {
			return nil, "", fmt.Errorf("容器服务 %q 不是验证器，实际为 %T", candidate, instance)
		}
		return validator, scene, nil
	}
	return nil, "", fmt.Errorf("验证器 %q 解析失败: %w", validatorName, resolveErr)
}

func controllerValidationArguments(arguments []interface{}) (map[string]string, bool, error) {
	if len(arguments) > 2 {
		return nil, false, fmt.Errorf("Validate 最多接收 message 和 batch 两个可选参数")
	}
	messages := map[string]string(nil)
	batch := false
	if len(arguments) >= 1 && arguments[0] != nil {
		configured, ok := arguments[0].(map[string]string)
		if !ok {
			return nil, false, fmt.Errorf("Validate message 参数必须是 map[string]string，实际为 %T", arguments[0])
		}
		messages = configured
	}
	if len(arguments) == 2 {
		configured, ok := arguments[1].(bool)
		if !ok {
			return nil, false, fmt.Errorf("Validate batch 参数必须是 bool，实际为 %T", arguments[1])
		}
		batch = configured
	}
	return messages, batch, nil
}

func controllerValidationBatch(controller *Controller, arguments []interface{}) bool {
	_, batch, err := controllerValidationArguments(arguments)
	if err != nil {
		panic(err)
	}
	return batch || controller != nil && controller.batchValidate
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
	return c.App.lang.Get(key, v, lang)
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
