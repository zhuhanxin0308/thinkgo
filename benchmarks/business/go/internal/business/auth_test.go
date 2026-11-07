package business

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"
)

// signedTestToken 独立构造测试凭证，避免用被测签名函数验证自身。
func signedTestToken(secret string, tenant, user, expiry int64) string {
	payload := fmt.Sprintf("%d.%d.%d", tenant, user, expiry)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(payload))
	return "Bearer " + payload + "." + hex.EncodeToString(mac.Sum(nil))
}

func TestAuthenticateEnforcesIdentityAndExpiry(t *testing.T) {
	limits := testDataset(t)
	secret := "test-only-signing-key-with-at-least-32-bytes"
	now := time.Unix(1800000000, 0)
	valid := signedTestToken(secret, 2, 1007, now.Unix()+60)
	principal, err := authenticate(valid, secret, now, limits)
	if err != nil || principal.TenantID != 2 || principal.UserID != 1007 {
		t.Fatalf("合法租户凭证未通过: principal=%+v error=%v", principal, err)
	}
	cases := map[string]string{
		"缺少凭证":  "",
		"错误密钥":  signedTestToken("different-secret", 2, 1007, now.Unix()+60),
		"已经过期":  signedTestToken(secret, 2, 1007, now.Unix()-1),
		"到期边界":  signedTestToken(secret, 2, 1007, now.Unix()),
		"跨租户身份": signedTestToken(secret, 1, 1007, now.Unix()+60),
		"未知租户":  signedTestToken(secret, 5, 4001, now.Unix()+60),
		"无效用户":  signedTestToken(secret, 1, 0, now.Unix()+60),
		"格式错误":  "Bearer 1.1.1.not-a-signature",
		"方案错误":  "Basic abc",
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := authenticate(token, secret, now, limits); err == nil {
				t.Fatal("无效凭证被接受")
			} else if code, _ := ErrorResult(err); code != 401 {
				t.Fatalf("认证错误应返回401，实际%d", code)
			}
		})
	}
}
