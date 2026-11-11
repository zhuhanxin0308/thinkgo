package middleware

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/exception"
)

// TestRecoveryWildcardAPIResponse 验证通配客户端经过真实 HTTP 恢复链仍收到 JSON 错误。
func TestRecoveryWildcardAPIResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recovery := &Recovery{App: &recoveryTestApp{debug: false}}
		response := recovery.Handle(fwcontext.MustNewRequest(r), func(*fwcontext.Request) *fwcontext.Response {
			panic(exception.NewHttpException(http.StatusForbidden, "forbidden"))
		})
		if err := response.Send(w); err != nil {
			t.Errorf("发送恢复响应失败: %v", err)
		}
	}))
	defer server.Close()
	for _, test := range []struct{ accept, contentType string }{
		{"*/*", "application/json"}, {"text/plain;q=1,*/*;q=0.1", "text/plain"},
		{"text/plain;q=0.1,application/json;q=0.9", "application/json"},
	} {
		t.Run(test.accept, func(t *testing.T) {
			request, err := http.NewRequest(http.MethodGet, server.URL+"/api/orders", nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Accept", test.accept)
			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusForbidden || !strings.HasPrefix(response.Header.Get("Content-Type"), test.contentType) {
				t.Fatalf("恢复响应类型或状态不正确: %d %v", response.StatusCode, response.Header)
			}
			if test.contentType == "text/plain" {
				body, err := io.ReadAll(response.Body)
				if err != nil || string(body) != "forbidden" {
					t.Fatalf("纯文本错误体无效: %q %v", body, err)
				}
				return
			}
			var body map[string]interface{}
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil || body["msg"] != "forbidden" {
				t.Fatalf("JSON 错误体无效: %v %v", body, err)
			}
		})
	}
}

// TestRecoveryDebugGenericErrorMedia 验证本机调试模式也遵守纯文本、JSON 与 HTML 偏好。
func TestRecoveryDebugGenericErrorMedia(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recovery := &Recovery{App: &recoveryTestApp{debug: true}}
		response := recovery.Handle(fwcontext.MustNewRequest(r), func(*fwcontext.Request) *fwcontext.Response {
			panic("debug failure")
		})
		if err := response.Send(w); err != nil {
			t.Errorf("发送调试响应失败: %v", err)
		}
	}))
	defer server.Close()
	for _, test := range []struct{ accept, contentType string }{
		{"text/plain;q=1,application/json;q=0.9,text/html;q=0", "text/plain"},
		{"text/plain", "text/plain"},
		{"text/plain;q=0.5,application/json;q=0.9", "application/json"},
		{"text/plain;q=0.5,text/html;q=0.9,application/json;q=0.1", "text/html"},
	} {
		t.Run(test.accept, func(t *testing.T) {
			request, err := http.NewRequest(http.MethodGet, server.URL+"/error", nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Accept", test.accept)
			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil || response.StatusCode != http.StatusInternalServerError || !strings.HasPrefix(response.Header.Get("Content-Type"), test.contentType) {
				t.Fatalf("调试响应协商错误: status=%d type=%s err=%v", response.StatusCode, response.Header.Get("Content-Type"), err)
			}
			if !strings.Contains(string(body), "debug failure") {
				t.Fatalf("本机调试详情丢失: %q", body)
			}
		})
	}
}
