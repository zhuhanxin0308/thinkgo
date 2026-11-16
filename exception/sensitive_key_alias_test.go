package exception

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestDebugRedactsCredentialAliasesAcrossRequestFormats(t *testing.T) {
	aliases := []string{"pwd", "db_pass", "passphrase", "MasterKey", "user_otp", "pin", "CSRF", "key", "X-Auth", "X-Pwd"}
	headers := make(http.Header)
	form := url.Values{"shipping": {"visible"}}
	input := map[string]string{"shipping": "visible"}
	for _, key := range aliases {
		headers.Set(key, "credential-test-value")
		form.Set(key, "credential-test-value")
		input[key] = "credential-test-value"
	}
	masked := sanitizeHeaders(headers)
	for _, key := range aliases {
		if values := masked[http.CanonicalHeaderKey(key)]; len(values) != 1 || values[0] != redactedPlaceholder {
			t.Errorf("敏感请求头 %q 泄露", key)
		}
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	sanitized, ok := sanitizeJSONBody(string(encoded))
	if !ok || strings.Contains(sanitized, "credential-test-value") || !strings.Contains(sanitized, "visible") {
		t.Error("异常 JSON 凭据未遮蔽或普通字段被移除")
	}
	sanitized = sanitizeRequestBody(form.Encode(), "application/x-www-form-urlencoded")
	if strings.Contains(sanitized, "credential-test-value") || !strings.Contains(sanitized, "shipping=visible") {
		t.Error("调试表单凭据未遮蔽或普通字段被移除")
	}
}
