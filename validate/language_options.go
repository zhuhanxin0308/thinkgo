package validate

import (
	"fmt"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/v3/lang"
)

// WithLang 指定单次验证的语言服务，不替换 SetLang 保存的默认服务。
func WithLang(language *lang.Lang) Option {
	return func(options *validationOptions) error {
		if language == nil {
			return fmt.Errorf("%w: 语言服务不能为空", ErrInvalidOption)
		}
		if options.language != nil && options.language != language {
			return fmt.Errorf("%w: 不能同时指定不同语言服务", ErrInvalidOption)
		}
		options.language = language
		return nil
	}
}

// WithLocale 指定本次调用的请求语言，空字符串沿用语言服务当前语言和既有回退规则。
// 不修改共享 Lang 的当前语言；重复的不同语言选项返回配置错误。
func WithLocale(locale string) Option {
	normalized := strings.ToLower(strings.TrimSpace(locale))
	return func(options *validationOptions) error {
		if options.localeSet && options.locale != normalized {
			return fmt.Errorf("%w: 不能同时指定不同请求语言", ErrInvalidOption)
		}
		options.locale, options.localeSet = normalized, true
		return nil
	}
}
