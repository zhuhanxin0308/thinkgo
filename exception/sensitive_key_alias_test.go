package exception

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestDebugRedactsCredentialAliasesAcrossRequestFormats(t *testing.T) {
	headerAliases := []string{
		"pwd", "db_pass", "passphrase", "MasterKey", "user_otp", "pin", "CSRF",
		"key", "X-Auth", "X-Pwd", "pass", "user_pass", "login-pass", "auth", "account.auth",
		"db:pass", "oauth:secret",
	}
	payloadAliases := append([]string(nil), headerAliases...)
	payloadAliases = append(payloadAliases, "userPwd", "UserPwd", "loginPass", "pinCode", "otpCode", "basicAuth")
	headers := make(http.Header)
	form := url.Values{"shipping": {"visible"}, "passport": {"visible-doc"}, "author": {"visible-author"}}
	input := map[string]string{"shipping": "visible", "passport": "visible-doc", "author": "visible-author"}
	for _, key := range headerAliases {
		headers.Set(key, "credential-test-value")
	}
	for _, key := range payloadAliases {
		form.Set(key, "credential-test-value")
		input[key] = "credential-test-value"
	}
	masked := sanitizeHeaders(headers)
	for _, key := range headerAliases {
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

func TestPublic4xxExceptionRedactsShortCredentialAliasesInMessage(t *testing.T) {
	handler := &Handle{App: &mockExceptionApp{debug: false}}
	rawMsg := "invalid input: pass=example-value auth=example-token user_pass=example-pass login.auth=example-login passport=P1001 author=bob auth_status=rejected pass_rate=0.5"
	for _, errCase := range []interface{}{
		NewHttpException(http.StatusBadRequest, rawMsg),
		NewBusinessException(http.StatusUnprocessableEntity, rawMsg),
		NewValidateException(rawMsg, "auth"),
	} {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "http://example.com/api/login", nil)
		req.Header.Set("Accept", "application/json")
		handler.Render(recorder, req, errCase)
		body := recorder.Body.String()
		for _, secret := range []string{"example-value", "example-token", "example-pass", "example-login"} {
			if strings.Contains(body, secret) {
				t.Fatalf("公开 4xx 响应泄露凭据别名值 %q: %s", secret, body)
			}
		}
		for _, ordinary := range []string{"passport=P1001", "author=bob", "auth_status=rejected", "pass_rate=0.5"} {
			if !strings.Contains(body, ordinary) {
				t.Fatalf("公开 4xx 响应误遮蔽普通字段 %q: %s", ordinary, body)
			}
		}
	}
}
