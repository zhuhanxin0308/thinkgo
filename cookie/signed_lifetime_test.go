package cookie

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestSignedCookieBindsEffectiveLifetime 验证实际写入选项和全局上限共同约束重放时间。
func TestSignedCookieBindsEffectiveLifetime(t *testing.T) {
	secret := strings.Repeat("s", minCookieSecretBytes)
	factory, err := NewCookie(map[string]interface{}{"secret": secret, "expire": 10, "sign_max_age": 60})
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range []struct {
		name    string
		expire  int
		elapsed time.Duration
		wantErr error
	}{
		{"短期 Cookie 过期", 5, 6 * time.Second, ErrExpiredCookieSignature},
		{"单次期限覆盖默认期限", 40, 20 * time.Second, nil},
		{"超过全局签名上限", 120, 61 * time.Second, ErrExpiredCookieSignature},
		{"删除 Cookie 的签名立即失效", -1, 0, ErrExpiredCookieSignature},
	} {
		t.Run(sample.name, func(t *testing.T) {
			header, err := factory.BuildHeader("sid", "value", CookieOptions{Expire: sample.expire})
			if err != nil {
				t.Fatal(err)
			}
			written, err := http.ParseSetCookie(header)
			if err != nil || written.MaxAge != sample.expire {
				t.Fatalf("浏览器 Cookie 的 Max-Age 出错: %v %v", written, err)
			}
			segments := strings.Split(written.Value, ".")
			if len(segments) != 5 || segments[0] != "v2" {
				t.Fatalf("新写入应使用绑定有效期的签名版本: %q", written.Value)
			}
			at, err := strconv.ParseInt(segments[2], 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			issued := time.Unix(at, 0)
			got, err := unsignCookieValue("sid", written.Value, secret, factory.config.SignMaxAge, factory.signingAge(factory.config.Expire), issued.Add(sample.elapsed))
			if !errors.Is(err, sample.wantErr) || sample.wantErr == nil && got != "value" {
				t.Errorf("Cookie 验签有效期错误: %q %v", got, err)
			}
			segments[3] = strconv.Itoa(factory.config.SignMaxAge + 1)
			if _, err := unsignCookieValue("sid", strings.Join(segments, "."), secret, factory.config.SignMaxAge, factory.signingAge(factory.config.Expire), issued); !errors.Is(err, ErrInvalidCookieSignature) {
				t.Errorf("篡改签名期限应被拒绝: %v", err)
			}
		})
	}
}

// TestLegacySignedCookieUsesOriginalLifetime 用旧版签名固定样本验证升级兼容。
func TestLegacySignedCookieUsesOriginalLifetime(t *testing.T) {
	const legacy = "v1.dmFsdWU.1700000000.bbLLp39tri6GvI3eWjUF9nJtZ_LtFtEd657EEVN-2gI"
	secret := strings.Repeat("s", minCookieSecretBytes)
	issued := time.Unix(1700000000, 0)
	if got, err := unsignCookieValue("sid", legacy, secret, 60, 10, issued.Add(9*time.Second)); err != nil || got != "value" {
		t.Fatalf("旧版 Cookie 应在原期限验签成功: %q %v", got, err)
	}
	if _, err := unsignCookieValue("sid", legacy, secret, 60, 10, issued.Add(11*time.Second)); !errors.Is(err, ErrExpiredCookieSignature) {
		t.Fatalf("旧版 Cookie 应在原期限结束后失效: %v", err)
	}
}
