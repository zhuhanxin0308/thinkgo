package framework

import (
	"net/http"

	"github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/lang"
)

// LangMiddleware 按显式查询参数、Cookie、自定义请求头和浏览器偏好的顺序选择请求语言。
func (app *App) LangMiddleware() func(req *context.Request, next func(*context.Request) *context.Response) *context.Response {
	if app == nil {
		return loadLangPack(nil)
	}
	return loadLangPack(app.lang)
}

func loadLangPack(language *lang.Lang) func(req *context.Request, next func(*context.Request) *context.Response) *context.Response {
	return func(req *context.Request, next func(*context.Request) *context.Response) *context.Response {
		selected := ""
		if language != nil {
			config := language.RequestDetectionConfig()
			queryValue, cookieValue, headerValue, acceptLanguage := "", "", "", ""
			if req != nil {
				// 无查询串和请求头时直接使用默认语言，避免为普通健康请求创建 Query、Cookie 和 Header 快照。
				raw := req.Raw()
				if raw == nil || (raw.URL == nil || raw.URL.RawQuery == "") && len(raw.Header) == 0 {
					selected = language.DetectLanguage("", "", "", "")
				} else {
					queryValue = req.Get(config.DetectVariable)
					if config.UseCookie {
						cookieValue = req.Cookie(config.CookieVariable)
					}
					headerValue = req.Header(config.HeaderVariable)
					if config.AutoDetectBrowser {
						acceptLanguage = req.Header("Accept-Language")
					}
					selected = language.DetectLanguage(queryValue, cookieValue, headerValue, acceptLanguage)
				}
			}
		}

		if req != nil {
			req.Set(LangRequestKey, selected)
		}
		if next == nil {
			return context.NewResponse().Code(http.StatusInternalServerError).Content(http.StatusText(http.StatusInternalServerError))
		}
		return next(req)
	}
}
