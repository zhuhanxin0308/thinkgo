package exception

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	frameworkVersion "github.com/zhuhanxin0308/thinkgo/v3/version"
)

// TestEmbeddedDebugTemplateRendersWithoutDiskTemplateAndPreservesSecurityBoundaries
// 验证独立项目未手工复制 framework/exception/tpl/exception.html 时，本机 Debug HTML
// 自动回退到内嵌完整调试模板；自定义模板优先、损坏自定义模板安全降级及来源边界保持不变。
func TestEmbeddedDebugTemplateRendersWithoutDiskTemplateAndPreservesSecurityBoundaries(t *testing.T) {
	isolatedProject := t.TempDir()
	t.Chdir(isolatedProject)

	logger := &mockExceptionLogger{}
	handler := &Handle{
		App:    &mockExceptionApp{debug: true},
		Log:    logger,
		TplDir: filepath.Join(isolatedProject, "framework", "exception", "tpl"),
	}

	secretToken := strings.Join([]string{"secret", "debug", "token"}, "-")
	formBody := url.Values{
		"password": []string{"top-secret-password"},
		"note":     []string{"visible-field"},
	}.Encode()

	localRequest := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8000/orders?debug=1", strings.NewReader(formBody))
		req.RemoteAddr = "127.0.0.1:54321"
		req.Header.Set("Accept", "text/html,application/xhtml+xml")
		req.Header.Set("Authorization", "Bearer "+secretToken)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return req
	}

	recorder := httptest.NewRecorder()
	if err := handler.Render(recorder, localRequest(), errors.New("boom from scaffold project")); err != nil {
		t.Fatalf("默认内嵌调试模板渲染失败: %v", err)
	}
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("状态码错误: got=%d want=500", recorder.Code)
	}
	body := recorder.Body.String()
	for _, want := range []string{
		"boom from scaffold project",
		"请求信息",
		"请求头",
		"运行环境",
		"POST",
		frameworkVersion.Framework,
		"[REDACTED]",
		"visible-field",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("默认内嵌调试页缺少关键诊断片段 %q，实际响应: %s", want, body)
		}
	}
	for _, forbidden := range []string{secretToken, "top-secret-password"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("内嵌调试页泄露了敏感值 %q", forbidden)
		}
	}
	if len(logger.errorCalls) != 0 {
		t.Fatalf("磁盘模板不存在时应无报错回退到内嵌模板，实际错误日志次数=%d", len(logger.errorCalls))
	}

	// 1. 宿主项目放置自定义模板时优先覆盖内嵌默认模板。
	customTplPath := filepath.Join(handler.TplDir, "exception.html")
	if err := os.MkdirAll(filepath.Dir(customTplPath), 0o750); err != nil {
		t.Fatalf("创建自定义模板目录失败: %v", err)
	}
	if err := os.WriteFile(customTplPath, []byte(`<!DOCTYPE html><html><body>CUSTOM-TPL: {{.Message}} | {{.Request.Method}}</body></html>`), 0o600); err != nil {
		t.Fatalf("写入自定义模板失败: %v", err)
	}
	customRecorder := httptest.NewRecorder()
	if err := handler.Render(customRecorder, localRequest(), errors.New("custom override")); err != nil {
		t.Fatalf("渲染自定义模板失败: %v", err)
	}
	if !strings.Contains(customRecorder.Body.String(), "CUSTOM-TPL: custom override | POST") {
		t.Fatalf("宿主自定义模板未优先于内嵌模板生效: %s", customRecorder.Body.String())
	}

	// 2. 已存在的自定义模板语法损坏时必须报告异常并安全降级到精简页，不能掩盖配置错误。
	if err := os.WriteFile(customTplPath, []byte(`<!DOCTYPE html>{{`), 0o600); err != nil {
		t.Fatalf("写入损坏模板失败: %v", err)
	}
	brokenRecorder := httptest.NewRecorder()
	if err := handler.Render(brokenRecorder, localRequest(), errors.New("broken custom tpl")); err != nil {
		t.Fatalf("损坏模板降级渲染失败: %v", err)
	}
	if !strings.Contains(brokenRecorder.Body.String(), "broken custom tpl") || strings.Contains(brokenRecorder.Body.String(), "CUSTOM-TPL") {
		t.Fatalf("损坏自定义模板未进入安全降级页: %s", brokenRecorder.Body.String())
	}
	if len(logger.errorCalls) == 0 {
		t.Fatal("损坏自定义模板应通过 Report 记录模板解析错误")
	}

	// 3. 删除损坏模板后重新恢复内嵌模板，且非回环/不可信代理/非 Debug 模式严禁暴露调试页。
	if err := os.Remove(customTplPath); err != nil {
		t.Fatalf("清理自定义模板失败: %v", err)
	}
	remoteReq := localRequest()
	remoteReq.RemoteAddr = "203.0.113.25:41000"
	remoteRecorder := httptest.NewRecorder()
	if err := handler.Render(remoteRecorder, remoteReq, errors.New("internal secret detail")); err != nil {
		t.Fatalf("渲染公网请求失败: %v", err)
	}
	if remoteRecorder.Body.String() != internalServerErrorText {
		t.Fatalf("非本机请求不得暴露任何调试页: %q", remoteRecorder.Body.String())
	}

	proxiedReq := localRequest()
	proxiedReq.Header.Set("X-Forwarded-For", "198.51.100.9")
	proxiedRecorder := httptest.NewRecorder()
	if err := handler.Render(proxiedRecorder, proxiedReq, errors.New("internal secret detail")); err != nil {
		t.Fatalf("渲染代理请求失败: %v", err)
	}
	if proxiedRecorder.Body.String() != internalServerErrorText {
		t.Fatalf("非本机代理转发请求不得暴露调试页: %q", proxiedRecorder.Body.String())
	}

	prodHandler := &Handle{
		App:    &mockExceptionApp{debug: false},
		TplDir: handler.TplDir,
	}
	prodRecorder := httptest.NewRecorder()
	if err := prodHandler.Render(prodRecorder, localRequest(), errors.New("internal secret detail")); err != nil {
		t.Fatalf("生产模式渲染失败: %v", err)
	}
	if prodRecorder.Body.String() != internalServerErrorText {
		t.Fatalf("生产模式不得暴露调试页: %q", prodRecorder.Body.String())
	}
}
