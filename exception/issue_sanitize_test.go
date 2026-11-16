package exception

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestDebugRequestBodyRedactsXMLAndHidesOpaqueText 验证调试页不暴露结构化或文本中的凭据。
func TestDebugRequestBodyRedactsXMLAndHidesOpaqueText(t *testing.T) {
	const payload = `<?xml version="1.0"?><login xmlns:s="urn:auth" s:access_token="hidden-attribute"><name>public</name><s:Password><![CDATA[hidden-element]]><nested>hidden-child</nested></s:Password><!--hidden-comment--></login>`
	for _, mediaType := range []string{"application/xml; charset=utf-8", "text/xml", "application/vnd.example+xml"} {
		t.Run(mediaType, func(t *testing.T) {
			raw := httptest.NewRequest(http.MethodPost, "http://localhost", strings.NewReader(payload))
			raw.Header.Set("Content-Type", mediaType)
			output := debugRequestBody(raw)
			if strings.Contains(output, "hidden-") || !strings.Contains(output, "<name>public</name>") ||
				strings.Count(output, redactedPlaceholder) != 2 {
				t.Fatalf("XML 脱敏结果错误: %q", output)
			}
			var element struct{ XMLName xml.Name }
			if err := xml.Unmarshal([]byte(output), &element); err != nil || element.XMLName.Local != "login" {
				t.Fatalf("脱敏后的 XML 应保留合法文档结构: %v", err)
			}
			if restored, err := io.ReadAll(raw.Body); err != nil || string(restored) != payload {
				t.Fatalf("调试处理改变了请求体: %v", err)
			}
		})
	}
	for _, sample := range []struct{ mediaType, body, want string }{
		{"text/plain", payload, "[TEXT BODY OMITTED]"},
		{"text/html", payload, "[TEXT BODY OMITTED]"},
		{"application/xml", "<password>hidden-unclosed", "[INVALID XML BODY OMITTED]"},
		{"application/xml", "<a></a><password>hidden-second-root</password>", "[INVALID XML BODY OMITTED]"},
		{"application/xml", "<name>" + strings.Repeat("x", maxDebugRequestBodyBytes) + "</name>", "[INVALID XML BODY OMITTED]"},
	} {
		raw := httptest.NewRequest(http.MethodPost, "http://localhost", strings.NewReader(sample.body))
		raw.Header.Set("Content-Type", sample.mediaType)
		if output := debugRequestBody(raw); output != sample.want {
			t.Errorf("正文应合规隐藏，媒体类型 %s，实际长度 %d", sample.mediaType, len(output))
		}
	}
}

// TestXMLRedactsSplitTextAndExpandedNamespaces 验证解析器分片和命名空间不能重新拼出秘密。
func TestXMLRedactsSplitTextAndExpandedNamespaces(t *testing.T) {
	const body = `<p:root xmlns:p="https://example.test/?token=namespace-secret" p:note="visible"><p:message>password=<![CDATA[split-secret]]></p:message></p:root>`
	output, ok := sanitizeXMLBody(body)
	if !ok || strings.Contains(output, "namespace-secret") || strings.Contains(output, "split-secret") ||
		!strings.Contains(output, "visible") || !strings.Contains(output, redactedPlaceholder) {
		t.Fatalf("XML 解析分片或扩展命名空间未脱敏: %q", output)
	}
}

func TestXMLRedactsCompleteValuesWithDecodedCredentialSeparators(t *testing.T) {
	const body = `<root note="token=first&#9;hidden-attribute"><message>password=first&#32;hidden-element</message><name>still visible</name></root>`
	output, ok := sanitizeXMLBody(body)
	if !ok || strings.Contains(output, "hidden-") || strings.Contains(output, "first") ||
		strings.Count(output, redactedPlaceholder) != 2 || !strings.Contains(output, "still visible") {
		t.Fatalf("XML 解析实体后必须隐藏含凭据的完整值: %q", output)
	}
}

func TestXMLRedactsCredentialAssignmentBeforeLeadingXMLWhitespace(t *testing.T) {
	const body = `<root note="Token :&#10;hidden-attribute"><message>password=&#10;hidden-element</message><other>metric=visible</other></root>`
	output, ok := sanitizeXMLBody(body)
	if !ok || strings.Contains(output, "hidden-") || strings.Count(output, redactedPlaceholder) != 2 ||
		!strings.Contains(output, "metric=visible") {
		t.Fatalf("XML 中凭据键后面的换行不能隐藏敏感赋值边界: %q", output)
	}
}

// TestXMLHidesMixedContentAndNamespaceCredentials 验证混合正文和命名空间别名不会暴露原值。
func TestXMLHidesMixedContentAndNamespaceCredentials(t *testing.T) {
	const mixed = "<message>password=<value>mixed-secret</value></message>"
	if output, ok := sanitizeXMLBody(mixed); !ok || output != "[MIXED XML BODY OMITTED]" {
		t.Errorf("无法安全归属字段的混合正文应隐藏: %q", output)
	}
	const namespace = `<root xmlns:token="urn:namespace-secret" token:note="visible"><token:value>public</token:value></root>`
	output, ok := sanitizeXMLBody(namespace)
	if !ok || strings.Contains(output, "namespace-secret") || !strings.Contains(output, "public") || !strings.Contains(output, "visible") {
		t.Fatalf("命名空间 URI 应隔离并保留其他结构化字段: %q", output)
	}
	var parsed struct{ XMLName xml.Name }
	if err := xml.Unmarshal([]byte(output), &parsed); err != nil {
		t.Fatalf("脱敏后的诊断 XML 不合法: %v", err)
	}
}

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
