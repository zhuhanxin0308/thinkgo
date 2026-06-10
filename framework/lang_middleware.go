package framework

import (
	"strings"
	"thinkgo/framework/context"
)

// LoadLangPack middleware to detect and load language
func (app *App) LoadLangPack() func(req *context.Request, next func(*context.Request) *context.Response) *context.Response {
	return func(req *context.Request, next func(*context.Request) *context.Response) *context.Response {
		// 语言检测优先级：GET参数 > Cookie（用户主动设置）> Accept-Language（浏览器语言）> 默认语言
		lang := ""

		// 1. GET参数（用于切换语言）
		if l := req.Get("lang"); l != "" {
			lang = l
		}

		// 2. Cookie（用户之前主动设置的语言偏好）
		// 直接从请求中读取 Cookie，避免依赖全局 Cookie 管理器（并发安全）
		if lang == "" {
			lang = req.Cookie("lang")
		}

		// 3. Accept-Language（浏览器语言设置）
		if lang == "" {
			if l := req.Header("Accept-Language"); l != "" {
				// 解析Accept-Language，如 "en-US,en;q=0.9,zh-CN;q=0.8"
				// 按优先级遍历所有语言偏好
				parts := strings.Split(l, ",")
				for _, part := range parts {
					// 提取语言标签，去除权重部分（如 "en-US;q=0.9" -> "en-US"）
					browserLang := strings.ToLower(strings.TrimSpace(strings.Split(part, ";")[0]))
					
					// 1. 先尝试完整匹配（如 en-us）
					if app.Lang.HasLang(browserLang) {
						lang = browserLang
						break
					}
					
					// 2. 如果是带地区码的语言（如 en-us），尝试匹配主语言码（如 en）
					if strings.Contains(browserLang, "-") {
						mainLang := strings.Split(browserLang, "-")[0]
						if app.Lang.HasLang(mainLang) {
							lang = mainLang
							break
						}
						// 3. 尝试查找同语言族的语言包（如 en -> en-us）
						if matchedLang := app.Lang.FindLangByPrefix(mainLang); matchedLang != "" {
							lang = matchedLang
							break
						}
					} else {
						// 4. 如果只有主语言码（如 en），尝试查找同语言族的语言包（如 en -> en-us）
						if matchedLang := app.Lang.FindLangByPrefix(browserLang); matchedLang != "" {
							lang = matchedLang
							break
						}
					}
				}
			}
		}

		// 4. 默认语言
		if lang == "" {
			lang = app.Lang.GetDefaultLang()
		}

		// 将当前语言存储到请求上下文（并发安全，不修改全局状态）
		req.Set(LangRequestKey, lang)

		return next(req)
	}
}
