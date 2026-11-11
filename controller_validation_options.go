package framework

import "github.com/zhuhanxin0308/thinkgo/v3/validate"

// SetStrictValidate 配置控制器的严格字段扩展，保留 ThinkPHP 风格 Validate 的四参数签名。
// 控制器按请求持有，初始化时配置；该开关不修改共享验证器，也不修改输入数据。
func (c *Controller) SetStrictValidate(strict ...bool) *Controller {
	if c == nil {
		return c
	}
	enabled := true
	if len(strict) > 0 {
		enabled = strict[0]
	}
	c.strictValidate = enabled
	return c
}

// controllerValidationOptions 为异常式及显式结果入口提供一致的请求级配置。
func (c *Controller) controllerValidationOptions() []validate.Option {
	var options []validate.Option
	if c == nil {
		return options
	}
	if c.strictValidate {
		options = append(options, validate.DisallowUnknownFields())
	}
	if c.App != nil {
		options = append(options, validate.WithLocation(c.App.Location()))
		if c.App.lang != nil {
			options = append(options, validate.WithLang(c.App.lang))
		}
	}
	if c.Request != nil {
		options = append(options, validate.WithLocale(c.GetLang()))
	}
	return options
}
