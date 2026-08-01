package log

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestLogTypedContextSnapshotAndDeepRedaction 验证类型化集合、结构体指针和 HTTP 头都会快照并深层脱敏。
// TestLogRedactsKeyMaterialAndCredentials 验证密钥材料与凭据字段不会因命名变体绕过脱敏。
func TestLogRedactsKeyMaterialAndCredentials(t *testing.T) {
	entry := &LogEntry{
		Time:  time.Now(),
		Level: "error",
		Context: map[string]interface{}{
			"private-key":      "private-secret",
			"signing_key":      "signing-secret",
			"encryptionKey":    "encryption-secret",
			"access_key":       "access-secret",
			"clientSecret":     "client-secret",
			"credential_value": "credential-secret",
		},
	}
	formatted := entry.FormatEntry()
	for _, secret := range []string{"private-secret", "signing-secret", "encryption-secret", "access-secret", "client-secret", "credential-secret"} {
		if strings.Contains(formatted, secret) {
			t.Fatalf("密钥或凭据值不应出现在日志中 %q，实际为 %s", secret, formatted)
		}
	}
}

// TestSanitizeErrorTextRedactsCredentialsInsideMessages 验证错误文本中的键值对也会被脱敏。
func TestSanitizeErrorTextRedactsCredentialsInsideMessages(t *testing.T) {
	secret := "error-text-password"
	sanitized := SanitizeErrorText("database password=" + secret + " host=db.example")
	if strings.Contains(sanitized, secret) || !strings.Contains(sanitized, logRedactedPlaceholder) {
		t.Fatalf("错误文本中的凭据必须脱敏: %q", sanitized)
	}
}

func TestLogTypedContextSnapshotAndDeepRedaction(t *testing.T) {
	driver := newMockDriver()
	logger := NewLog(driver)
	logger.SetFlushInterval(time.Hour)

	credentials := &logTestCredentials{
		Username: "visible-user",
		Password: "struct-password-secret",
		Headers: http.Header{
			"Authorization": []string{"Bearer header-secret"},
			"X-Request-ID":  []string{"request-123"},
		},
		Metadata: map[string][]string{
			"api_token": []string{"typed-map-secret"},
			"region":    []string{"cn-east"},
		},
	}
	logger.InfoCtx("typed context", map[string]interface{}{"credentials": credentials})

	credentials.Username = "mutated-user"
	credentials.Password = "mutated-password"
	credentials.Headers.Set("Authorization", "Bearer mutated-header")
	credentials.Metadata["region"][0] = "mutated-region"

	if err := logger.Flush(context.Background()); err != nil {
		t.Fatalf("刷盘类型化上下文失败: %v", err)
	}
	entries := driver.allEntries()
	if len(entries) != 1 {
		t.Fatalf("应保存 1 条日志，实际为 %d", len(entries))
	}

	formatted := entries[0].FormatEntry()
	for _, secret := range []string{
		"struct-password-secret",
		"header-secret",
		"typed-map-secret",
		"mutated-password",
		"mutated-header",
	} {
		if strings.Contains(formatted, secret) {
			t.Fatalf("深层脱敏后不应包含敏感值 %q，实际为 %s", secret, formatted)
		}
	}
	for _, expected := range []string{"visible-user", "request-123", "cn-east", logRedactedPlaceholder} {
		if !strings.Contains(formatted, expected) {
			t.Fatalf("快照日志应包含 %q，实际为 %s", expected, formatted)
		}
	}
	for _, mutated := range []string{"mutated-user", "mutated-region"} {
		if strings.Contains(formatted, mutated) {
			t.Fatalf("快照日志不应包含调用后的修改值 %q，实际为 %s", mutated, formatted)
		}
	}

	if err := logger.Close(); err != nil {
		t.Fatalf("关闭日志器失败: %v", err)
	}
}

// TestLogCircularContextDoesNotRecurseForever 验证循环引用不会导致快照或格式化无限递归。
func TestLogCircularContextDoesNotRecurseForever(t *testing.T) {
	driver := newMockDriver()
	logger := NewLog(driver)
	node := &logTestNode{Token: "cycle-secret"}
	node.Next = node

	logger.InfoCtx("circular context", map[string]interface{}{"node": node})
	if err := logger.Flush(context.Background()); err != nil {
		t.Fatalf("刷盘循环引用上下文失败: %v", err)
	}

	entries := driver.allEntries()
	if len(entries) != 1 {
		t.Fatalf("应保存 1 条日志，实际为 %d", len(entries))
	}
	formatted := entries[0].FormatEntry()
	if strings.Contains(formatted, "cycle-secret") {
		t.Fatalf("循环引用中的敏感字段应脱敏，实际为 %s", formatted)
	}
	if !strings.Contains(formatted, "[CIRCULAR]") {
		t.Fatalf("循环引用应使用明确标记终止，实际为 %s", formatted)
	}

	if err := logger.Close(); err != nil {
		t.Fatalf("关闭日志器失败: %v", err)
	}
}
