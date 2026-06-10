package exception

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestBusinessExceptionCarriesSafeDataAndHiddenCause 验证业务异常支持返回安全 data，
// 同时把内部 cause 留在日志上下文中，避免泄露给客户端。
func TestBusinessExceptionCarriesSafeDataAndHiddenCause(t *testing.T) {
	logger := &mockExceptionLogger{}
	handler := &Handle{
		App: &mockExceptionApp{debug: false},
		Log: logger,
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/api/orders", nil)
	req.Header.Set("Accept", "application/json")
	recorder := httptest.NewRecorder()

	handler.Render(
		recorder,
		req,
		NewBusinessException(1002, "库存不足").
			WithStatus(http.StatusConflict).
			WithData(map[string]interface{}{"field": "stock"}).
			WithCause(errors.New("sql: no rows in result set")),
	)

	body := recorder.Body.String()
	if recorder.Code != http.StatusConflict {
		t.Fatalf("业务异常应返回 409，实际为 %d", recorder.Code)
	}
	if !strings.Contains(body, `"code":1002`) {
		t.Fatalf("响应体应保留业务码，实际为 %s", body)
	}
	if !strings.Contains(body, `"field":"stock"`) {
		t.Fatalf("响应体应保留安全的结构化 data，实际为 %s", body)
	}
	if strings.Contains(body, "sql: no rows in result set") {
		t.Fatalf("响应体不应暴露内部 cause，实际为 %s", body)
	}
	if len(logger.warningCalls) != 1 {
		t.Fatalf("业务异常应记录 1 条 warning，实际为 %d", len(logger.warningCalls))
	}
	if logger.warningCalls[0].ctx["cause"] == nil {
		t.Fatal("业务异常日志应包含内部 cause")
	}
}
