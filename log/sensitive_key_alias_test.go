package log

import "testing"

func TestStructuredLogRedactsCredentialAliases(t *testing.T) {
	aliases := []string{"pwd", "db_pass", "passphrase", "MasterKey", "user_otp", "pin", "CSRF", "key", "X-Auth", "X-Pwd", "pass", "user_pass", "login-pass", "auth", "account.auth"}
	fields := map[string]interface{}{"shipping": "shipping-visible", "monkey": "monkey-visible", "public_key": "public-visible"}
	for _, alias := range aliases {
		fields[alias] = "credential-test-value"
	}
	nested := sanitizeLogContext(map[string]interface{}{"fields": fields})["fields"].(map[string]interface{})
	for _, alias := range aliases {
		if nested[alias] != logRedactedPlaceholder {
			t.Errorf("敏感字段 %q 未完全遮蔽", alias)
		}
	}
	for field, want := range map[string]string{"shipping": "shipping-visible", "monkey": "monkey-visible", "public_key": "public-visible"} {
		if nested[field] != want {
			t.Errorf("正常字段 %q 被错误遮蔽", field)
		}
	}
}
