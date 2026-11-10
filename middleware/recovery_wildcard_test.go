package middleware

import (
	"encoding/json"
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
	request, err := http.NewRequest(http.MethodGet, server.URL+"/api/orders", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Accept", "*/*")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden || !strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("恢复响应类型或状态不正确: %d %v", response.StatusCode, response.Header)
	}
	var body map[string]interface{}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil || body["msg"] != "forbidden" {
		t.Fatalf("JSON 错误体无效: %v %v", body, err)
	}
}
