package cookie

import "testing"

// TestParseConfigKeepsSecureDefaultsWhenSameSiteBlank 验证空的 SameSite 配置不会把安全默认值清空。
func TestParseConfigKeepsSecureDefaultsWhenSameSiteBlank(t *testing.T) {
	cfg := ParseConfig(map[string]interface{}{
		"samesite": "",
	})

	if cfg.SameSite != "Lax" {
		t.Fatalf("空 SameSite 应回退为 Lax，实际为 %q", cfg.SameSite)
	}
}
