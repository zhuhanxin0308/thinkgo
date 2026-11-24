package binding

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

// 可选 JSON 字段允许真正没有正文的请求，零字节回放流不能引入不支持的媒体类型错误。
func TestOptionalBodyAcceptsEmptyReplacementStream(t *testing.T) {
	raw := httptest.NewRequest(http.MethodPost, "/", nil)
	request, err := fwcontext.NewRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	raw.Body = io.NopCloser(strings.NewReader(""))
	var target struct {
		Name string `json:"name"`
	}
	if err := Bind(request, &target); err != nil {
		t.Fatalf("空流阻断可选正文绑定：%v", err)
	}
	if target.Name != "" {
		t.Fatalf("空正文产生了字段值：%q", target.Name)
	}
	if err := request.Cleanup(); err != nil {
		t.Fatal(err)
	}
}
