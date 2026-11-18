package log

import (
	"context"
	"strings"
	"testing"
)

func TestStructuredLogRedactsCredentialAliases(t *testing.T) {
	aliases := []string{
		"pwd", "db_pass", "passphrase", "MasterKey", "user_otp", "pin", "CSRF",
		"key", "X-Auth", "X-Pwd", "pass", "user_pass", "login-pass", "auth", "account.auth",
		"userPwd", "UserPwd", "loginPass", "pinCode", "otpCode", "basicAuth", "db:pass", "redis:auth", "oauth:secret",
	}
	safeFields := map[string]string{
		"shipping": "shipping-visible", "monkey": "monkey-visible", "public_key": "public-visible",
		"author": "author-visible", "passport": "passport-visible", "bypass": "bypass-visible",
		"compass": "compass-visible", "pinpoint": "pinpoint-visible", "output": "output-visible",
	}
	fields := make(map[string]interface{}, len(aliases)+len(safeFields))
	for field, value := range safeFields {
		fields[field] = value
	}
	for _, alias := range aliases {
		fields[alias] = "credential-test-value"
	}
	nested := sanitizeLogContext(map[string]interface{}{"fields": fields})["fields"].(map[string]interface{})
	for _, alias := range aliases {
		if nested[alias] != logRedactedPlaceholder {
			t.Errorf("敏感字段 %q 未完全遮蔽", alias)
		}
	}
	for field, want := range safeFields {
		if nested[field] != want {
			t.Errorf("正常字段 %q 被错误遮蔽", field)
		}
	}
}

func TestLogRecordSanitizesShortCredentialAliasesInFreeText(t *testing.T) {
	driver := newMockDriver()
	logger := NewLog(driver)
	t.Cleanup(func() { _ = logger.Close() })

	logger.Record("login failed: pass=example-secret auth=example-auth user_pass=example-userpass passport=visible-id author=alice auth_status=denied pass_rate=95", LevelWarning)
	if err := logger.Flush(context.Background()); err != nil {
		t.Fatalf("刷盘日志失败: %v", err)
	}
	entries := driver.allEntries()
	if len(entries) != 1 {
		t.Fatalf("应记录 1 条日志，实际为 %d", len(entries))
	}
	msg := entries[0].Message
	for _, leaked := range []string{"example-secret", "example-auth", "example-userpass"} {
		if strings.Contains(msg, leaked) {
			t.Fatalf("自由文本消息泄露凭据 %q: %s", leaked, msg)
		}
	}
	for _, preserved := range []string{"passport=visible-id", "author=alice", "auth_status=denied", "pass_rate=95"} {
		if !strings.Contains(msg, preserved) {
			t.Fatalf("普通业务字段 %q 被误脱敏: %s", preserved, msg)
		}
	}
}
