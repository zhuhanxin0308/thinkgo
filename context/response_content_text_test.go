package context

import (
	"net/http/httptest"
	"testing"
)

func TestContentDefaultsToPlainTextAndPreservesExplicitType(t *testing.T) {
	const markup = "<strong>来自用户的文本</strong>"
	for _, test := range []struct {
		name     string
		response *Response
		wantType string
	}{
		{"默认纯文本", NewResponse().Content(markup), "text/plain; charset=utf-8"},
		{"HTML 入口", NewResponse().HTML(markup), "text/html; charset=utf-8"},
		{"显式 HTML 类型", NewResponse().ContentType("text/html; charset=utf-8").Content(markup), "text/html; charset=utf-8"},
		{"显式其他类型", NewResponse().ContentType("text/csv").Content(markup), "text/csv"},
	} {
		t.Run(test.name, func(t *testing.T) {
			writer := httptest.NewRecorder()
			if err := test.response.Send(writer); err != nil {
				t.Fatal(err)
			}
			if got := writer.Result().Header.Get("Content-Type"); got != test.wantType || writer.Body.String() != markup {
				t.Fatalf("响应类型或内容错误: %q, %q", got, writer.Body.String())
			}
		})
	}
}
