package middleware

import (
	"net/http"
	"strings"
	"testing"

	htmlparser "golang.org/x/net/html"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/debug"
)

func TestIssue129TracePreservesInertHTMLContexts(t *testing.T) {
	marker := `<img id=trace-marker src=/audit-dummy>`
	for _, mode := range []string{"html", "console"} {
		for _, test := range []struct {
			name, document string
			inject         bool
		}{
			{"unicode", `<html><body><script type="application/json">{"v":"` + strings.Repeat("K", 25) + marker + `"}</script> </body></html>`, true},
			{"fake-body-in-json", `<html><body><script type="application/json">{"v":"</body>` + marker + `"}</script></html>`, false},
			{"unclosed-script", `<html><body><script type="application/json">{"v":"</body>` + marker, false},
			{"comment-and-uppercase", `<html><body><!-- </body>` + marker + ` --><p>ok</p></BODY></html>`, true},
			{"textarea", `<html><body><textarea></body>` + marker + `</textarea></body></html>`, true},
			{"template-without-body-close", `<html><body><template></body></template></html>`, false},
		} {
			t.Run(mode+"/"+test.name, func(t *testing.T) {
				if countTraceMarker(t, test.document) != 0 {
					t.Fatal("基线标记必须处于惰性上下文")
				}
				trace := &Trace{Debug: &debug.Debug{Enabled: true}, Type: mode}
				response := trace.Handle(newLocalTraceRequest(http.MethodGet, "http://example.com/page"), func(*fwcontext.Request) *fwcontext.Response {
					return fwcontext.NewResponse().ContentType("text/html", "utf-8").Content(test.document)
				})
				body := string(response.GetBody())
				if countTraceMarker(t, body) != 0 {
					t.Fatalf("Trace 把惰性标记激活成元素: %s", body)
				}
				if !test.inject {
					if body != test.document || response.Headers().Get("Cache-Control") != "" {
						t.Fatal("缺少真实正文结束标签时必须保留响应")
					}
					return
				}
				if body == test.document || response.Headers().Get("Cache-Control") != "no-store" {
					t.Fatal("正常 HTML 应仍有 Trace 输出和 no-store")
				}
			})
		}
	}
}

func TestTraceKeepsImplicitAndExplicitHTMLMediaType(t *testing.T) {
	const document = "<html><body><p>原始页面</p></body></html>"
	for _, explicit := range []string{"", "text/html; charset=utf-8; profile=local"} {
		response := fwcontext.NewResponse().Data(document)
		if explicit != "" {
			response.Header("Content-Type", explicit)
		}
		want := response.GetHeader("Content-Type")
		trace := &Trace{Debug: &debug.Debug{Enabled: true}, Type: "html"}
		result := trace.Handle(newLocalTraceRequest(http.MethodGet, "http://example.com/page"), func(*fwcontext.Request) *fwcontext.Response {
			return response
		})
		if got := result.GetHeader("Content-Type"); got != want || string(result.GetBody()) == document || result.GetHeader("Cache-Control") != "no-store" {
			t.Fatalf("Trace 注入应保留 HTML 类型并输出调试栏: type=%q want=%q", got, want)
		}
	}
}

func countTraceMarker(t *testing.T, content string) int {
	t.Helper()
	document, err := htmlparser.Parse(strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	var visit func(*htmlparser.Node)
	visit = func(node *htmlparser.Node) {
		if node.Type == htmlparser.ElementNode {
			for _, attribute := range node.Attr {
				if attribute.Key == "id" && attribute.Val == "trace-marker" {
					count++
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(document)
	return count
}
