package framework

import (
	"errors"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework/middleware"
)

// TestCreateAppSecurityHeadersStrictConfiguration 验证启用、安全默认值、未知字段和类型错误契约。
func TestCreateAppSecurityHeadersStrictConfiguration(t *testing.T) {
	handler, enabled, err := createAppSecurityHeaders(map[string]interface{}{"enable": true})
	if err != nil || !enabled || handler == nil {
		t.Fatalf("启用安全响应头失败: enabled=%t handler=%v err=%v", enabled, handler, err)
	}
	if _, _, err := createAppSecurityHeaders(map[string]interface{}{"enable": true, "unknown": true}); err == nil {
		t.Fatal("未知安全响应头字段必须被拒绝")
	}
	if _, _, err := createAppSecurityHeaders(map[string]interface{}{"enable": "true"}); err == nil {
		t.Fatal("错误 enable 类型必须被拒绝")
	}
	if _, _, err := createAppSecurityHeaders(map[string]interface{}{
		"enable":               true,
		"hsts_preload":         true,
		"hsts_max_age_seconds": 1,
	}); !errors.Is(err, middleware.ErrInvalidSecurityHeaders) {
		t.Fatalf("不安全 HSTS preload 必须被拒绝，实际为 %v", err)
	}
}
