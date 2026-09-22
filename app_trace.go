package framework

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/zhuhanxin0308/thinkgo/v3/debug"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
)

// createAppTrace 把 trace.php 的 type 和 channel 原样转换为 Trace 中间件配置。
func createAppTrace(configuration map[string]interface{}, manager *debug.Debug, app *App) (*middleware.Trace, error) {
	traceType := "Html"
	channel := ""
	if raw, exists := configuration["type"]; exists {
		value, ok := raw.(string)
		if !ok || strings.TrimSpace(value) == "" || hasTraceControl(value) {
			return nil, fmt.Errorf("trace.type 必须是安全的非空字符串")
		}
		switch {
		case strings.EqualFold(value, "Html"):
			traceType = "Html"
		case strings.EqualFold(value, "Console"):
			traceType = "Console"
		default:
			return nil, fmt.Errorf("trace.type %q 未注册", value)
		}
	}
	if raw, exists := configuration["channel"]; exists {
		value, ok := raw.(string)
		if !ok || hasTraceControl(value) {
			return nil, fmt.Errorf("trace.channel 必须是不含控制字符的字符串")
		}
		channel = value
	}
	location := defaultApplicationLocation()
	if app != nil {
		location = app.Location()
	}
	return &middleware.Trace{Debug: manager, Location: location, Type: traceType, Channel: channel}, nil
}

func hasTraceControl(value string) bool {
	if !utf8.ValidString(value) {
		return true
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}
