package cookie

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// FuzzSignedCookieIntegrity 验证签名往返、名称绑定和有效编码下的载荷篡改拒绝。
func FuzzSignedCookieIntegrity(f *testing.F) {
	for _, value := range []string{"", "用户\x00数据", "a.b=c;", "<script>"} {
		f.Add(value)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if len(value) > 512 || !utf8.ValidString(value) {
			return
		}
		const name, secret = "audit", "synthetic-cookie-fuzz-secret-20260922"
		now := time.Unix(1790000000, 0)
		signed, err := signCookieValue(name, value, secret, now)
		if err != nil {
			t.Fatal(err)
		}
		actual, err := unsignCookieValue(name, signed, secret, 60, now)
		if err != nil || actual != value {
			t.Fatalf("签名往返失败: %v", err)
		}
		if _, err := unsignCookieValue("other", signed, secret, 60, now); err == nil {
			t.Fatal("跨名称重放被接受")
		}
		parts := strings.Split(signed, ".")
		parts[1] = base64.RawURLEncoding.EncodeToString([]byte(value + "changed"))
		if _, err := unsignCookieValue(name, strings.Join(parts, "."), secret, 60, now); err == nil {
			t.Fatal("有效编码的载荷篡改被接受")
		}
	})
}
