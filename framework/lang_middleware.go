package framework

import (
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"thinkgo/framework/context"
)

const (
	maxAcceptLanguageBytes   = 8192
	maxAcceptLanguageEntries = 32
)

type weightedLanguage struct {
	tag      string
	quality  float64
	position int
}

// LoadLangPack 按显式查询参数、Cookie、自定义请求头和浏览器偏好的顺序选择请求语言。
func (app *App) LoadLangPack() func(req *context.Request, next func(*context.Request) *context.Response) *context.Response {
	return func(req *context.Request, next func(*context.Request) *context.Response) *context.Response {
		selected := ""
		if app != nil && app.Lang != nil {
			config := app.Lang.DetectionConfig()

			// 每个显式来源都必须通过已加载语言和允许列表校验，无效值继续回落到下一来源。
			if req != nil {
				selected = app.Lang.MatchLanguage(req.Get(config.DetectVariable))
				if selected == "" && config.UseCookie {
					selected = app.Lang.MatchLanguage(req.Cookie(config.CookieVariable))
				}
				if selected == "" {
					selected = app.Lang.MatchLanguage(req.Header(config.HeaderVariable))
				}
				if selected == "" && config.AutoDetectBrowser {
					selected = matchAcceptedLanguage(app, req.Header("Accept-Language"))
				}
			}

			if selected == "" {
				selected = app.Lang.GetDefaultLang()
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

func matchAcceptedLanguage(app *App, header string) string {
	if app == nil || app.Lang == nil {
		return ""
	}
	for _, candidate := range parseAcceptLanguage(header) {
		if candidate.tag == "*" {
			if matched := app.Lang.MatchLanguage(app.Lang.GetDefaultLang()); matched != "" {
				return matched
			}
			continue
		}
		if matched := app.Lang.MatchLanguage(candidate.tag); matched != "" {
			return matched
		}
	}
	return ""
}

func parseAcceptLanguage(header string) []weightedLanguage {
	if len(header) == 0 || len(header) > maxAcceptLanguageBytes {
		return nil
	}
	parts := strings.Split(header, ",")
	if len(parts) > maxAcceptLanguageEntries {
		return nil
	}

	languages := make([]weightedLanguage, 0, len(parts))
	for position, part := range parts {
		segments := strings.Split(part, ";")
		tag := strings.TrimSpace(segments[0])
		if tag == "" {
			continue
		}
		quality, valid := parseLanguageQuality(segments[1:])
		if !valid || quality <= 0 {
			continue
		}
		languages = append(languages, weightedLanguage{tag: tag, quality: quality, position: position})
	}

	// 相同权重保持请求头中的原始先后顺序，避免由排序实现引入不稳定选择。
	sort.SliceStable(languages, func(left, right int) bool {
		if languages[left].quality == languages[right].quality {
			return languages[left].position < languages[right].position
		}
		return languages[left].quality > languages[right].quality
	})
	return languages
}

func parseLanguageQuality(parameters []string) (float64, bool) {
	quality := 1.0
	found := false
	for _, parameter := range parameters {
		name, raw, exists := strings.Cut(strings.TrimSpace(parameter), "=")
		if !exists || !strings.EqualFold(strings.TrimSpace(name), "q") {
			continue
		}
		if found {
			return 0, false
		}
		found = true
		value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
			return 0, false
		}
		quality = value
	}
	return quality, true
}
