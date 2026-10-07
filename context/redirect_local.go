package context

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// RedirectLocal 仅接受以单个 / 开头的站内绝对路径，保留查询参数与片段。
// 拒绝外部 origin、反斜线、点路径段和编码后的分隔/控制字符歧义。
// 非法目标与 Redirect 的非法参数一样记录 ErrInvalidRedirect、清除 Location 并返回 500。
// 本方法不验证目标路由权限，也不能防止可信站内路由再次跳往外站。
func (r *Response) RedirectLocal(target string, code ...int) *Response {
	if r == nil {
		return nil
	}
	if !validLocalRedirect(target) || len(code) > 1 || (len(code) == 1 && !isRedirectResponseStatus(code[0])) {
		r.addError(fmt.Errorf("%w: 必须使用无歧义的站内路径", ErrInvalidRedirect))
		r.status = http.StatusInternalServerError
		if r.header != nil {
			r.header.values.Del("Location")
		}
		return r
	}
	return r.Redirect(target, code...)
}

func validLocalRedirect(target string) bool {
	if target == "" || !utf8.ValidString(target) || target != strings.TrimSpace(target) {
		return false
	}
	for _, char := range target {
		if unicode.IsControl(char) || char == '\\' {
			return false
		}
	}
	parsed, err := url.Parse(target)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.User != nil || parsed.Opaque != "" {
		return false
	}
	path := parsed.Path
	if !utf8.ValidString(path) || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return false
	}
	for _, char := range path {
		if unicode.IsControl(char) || char == '\\' {
			return false
		}
	}
	for _, part := range strings.Split(path, "/") {
		if part == "." || part == ".." {
			return false
		}
	}
	// 编码后的首部分隔符同样不得在解码后形成 authority。
	return strings.HasPrefix(target, "/") && !strings.HasPrefix(target, "//")
}
