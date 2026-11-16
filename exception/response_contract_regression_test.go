package exception

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestExceptionNegotiationUsesAllAcceptLines 验证多行与合并 Accept 的协商一致。
func TestExceptionNegotiationUsesAllAcceptLines(t *testing.T) {
	for _, test := range []struct {
		values []string
		path   string
		want   exceptionMedia
	}{
		{[]string{"text/html;q=0.3", "application/json;q=0.9"}, "/", exceptionMediaJSON},
		{[]string{"application/json;q=0.3", "text/plain;q=0.9"}, "/", exceptionMediaPlain},
		{[]string{"", ""}, "/api/orders", exceptionMediaJSON},
	} {
		req := httptest.NewRequest(http.MethodGet, "http://example.com"+test.path, nil)
		req.Header["Accept"] = test.values
		if got := exceptionResponseMedia(req); got != test.want {
			t.Errorf("Accept=%v: 得到 %v，期望 %v", test.values, got, test.want)
		}
	}
}

// TestSanitizeJSONBodyPreservesExactNumbers 验证脱敏保持非敏感数字的精度和类型。
func TestSanitizeJSONBodyPreservesExactNumbers(t *testing.T) {
	body := `{"id":9223372036854775807,"items":[9007199254740993],"token":9007199254740995}`
	got, ok := sanitizeJSONBody(body)
	if !ok || !strings.Contains(got, "9223372036854775807") || !strings.Contains(got, "[9007199254740993]") ||
		strings.Contains(got, "9007199254740995") || !strings.Contains(got, redactedPlaceholder) {
		t.Fatalf("脱敏修改了普通数字或泄露了敏感数字: %q", got)
	}
	if got, ok := sanitizeJSONBody(body + "  {}"); ok || got != "" {
		t.Fatalf("含尾随值的 JSON 应被拒绝: %q", got)
	}
}
