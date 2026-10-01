package exception

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestIssue41KeyMaterialIsSensitive(t *testing.T) {
	for _, key := range []string{"private_key", "private-key", "privateKey", "signing.key", "encryption_key", "credential", "client_secret", "access-key", "API_KEY"} {
		t.Run(key, func(t *testing.T) {
			body, ok := sanitizeJSONBody(`{"nested":[{"` + key + `":"hidden-material","public":"retained"}]}`)
			if !ok || strings.Contains(body, "hidden-material") || !strings.Contains(body, "retained") {
				t.Fatalf("failed sensitive key %s: %s", key, body)
			}
		})
	}
}

// The same policy protects URLs, form bodies and headers without changing the request.
func TestIssue41RequestChannelsUseSameRedaction(t *testing.T) {
	const secret = "hidden-material"
	for _, key := range []string{"private_key", "signing-key", "encryptionKey", "credentials", "X-Client.Secret", "X-Access-Key"} {
		t.Run(key, func(t *testing.T) {
			values := url.Values{key: {secret}, "public": {"retained"}}
			request := httptest.NewRequest(http.MethodGet, "http://localhost/?"+values.Encode(), nil)
			request.Header.Set(key, secret)
			request.Header.Set("X-Public", "retained")
			originalURL := request.URL.String()
			results := map[string]string{
				"url":     sanitizeRequestURL(request),
				"form":    sanitizeRequestBody(values.Encode(), "application/x-www-form-urlencoded"),
				"headers": fmt.Sprint(sanitizeHeaders(request.Header)),
			}
			for channel, output := range results {
				if strings.Contains(output, secret) || !strings.Contains(output, "retained") {
					t.Errorf("%s did not preserve redaction boundary: %s", channel, output)
				}
			}
			if request.Header.Get(key) != secret || request.URL.String() != originalURL {
				t.Fatal("sanitization changed request input")
			}
		})
	}
}
