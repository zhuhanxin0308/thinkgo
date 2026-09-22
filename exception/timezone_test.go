package exception

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestExceptionJSONNormalizesTimesToUTC 验证异常 JSON 响应不会绕过统一时间协议。
func TestExceptionJSONNormalizesTimesToUTC(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("加载测试时区失败: %v", err)
	}

	recorder := httptest.NewRecorder()
	value := time.Date(2026, time.July, 25, 0, 30, 0, 0, location)
	if err := writeJSON(recorder, http.StatusBadRequest, map[string]interface{}{
		"data": map[string]interface{}{"created_at": value},
	}); err != nil {
		t.Fatalf("异常 JSON 响应写入失败: %v", err)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `"created_at":"2026-07-24T16:30:00Z"`) {
		t.Fatalf("异常响应时间未统一为 UTC: %s", body)
	}
}
