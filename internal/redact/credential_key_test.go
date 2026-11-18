package redact

import "testing"

func TestCredentialAliasRespectsShortFieldBoundaries(t *testing.T) {
	for _, key := range []string{
		"pwd", "Db.Pass", "passphrase", "master-key", "account.otp", "pin_code", "CSRF",
		"Key", " X-Auth ", "X-Key", "Pass", "X-Pass", "credential", "credentials", "AppCredentials",
		"user_pass", "login-pass", "auth", "login.auth",
		"userPwd", "UserPwd", "loginPass", "pinCode", "otpCode", "basicAuth",
		"db:pass", "redis:auth", "oauth:secret", "oauth/secret", "PINCode", "OTPToken", "XAuth",
	} {
		if !CredentialAlias(key) {
			t.Errorf("未识别凭据别名 %q", key)
		}
	}
	for _, key := range []string{
		"", "   ", "---", "monkey", "shipping", "public_key", "key_count", "adoption",
		"x-author", "author", "authority", "compass", "passport", "bypass", "pinpoint",
		"output", "pass_rate", "auth_status", "passRate", "authStatus", "HTMLParser",
	} {
		if CredentialAlias(key) {
			t.Errorf("普通字段 %q 被误判为凭据", key)
		}
	}
}
