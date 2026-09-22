package binding

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
)

// BenchmarkBindSourceSelection 区分新请求首次绑定与已解析请求重复绑定，类型计划均预热。
// 原始 Header 和 URL 仅由基准读取，避免将测试夹具构造开销混入框架解析及来源复制成本。
func BenchmarkBindSourceSelection(b *testing.B) {
	const extraMetadataCount = 32
	for _, scenario := range []struct {
		name, media, body string
		metadata          bool
		target            any
	}{
		{name: "json", media: "application/json", body: `{"name":"Ada"}`, target: &struct {
			Name string `json:"name"`
		}{}},
		{name: "json_metadata", media: "application/json", body: `{"name":"Ada"}`, metadata: true, target: &struct {
			Name string `json:"name"`
		}{}},
		{name: "query_metadata", metadata: true, target: &struct {
			Page int `query:"page"`
		}{}},
		{name: "form_metadata", media: "application/x-www-form-urlencoded", body: "name=Ada&tags=one&tags=two", metadata: true, target: &struct {
			Name string   `form:"name"`
			Tags []string `form:"tags"`
		}{}},
		{name: "header_cookie", metadata: true, target: &struct {
			Token   string `header:"X-Token"`
			Session string `cookie:"session"`
		}{}},
	} {
		b.Run(scenario.name, func(b *testing.B) {
			header := http.Header{"X-Token": {"token"}}
			query, cookies := []string{"page=2"}, []string{"session=current"}
			if scenario.media != "" {
				header.Set("Content-Type", scenario.media)
			}
			if scenario.metadata {
				for index := range extraMetadataCount {
					name := fmt.Sprintf("unused%d", index)
					query = append(query, name+"=value")
					cookies = append(cookies, name+"=value")
					header.Set("X-"+name, "value")
				}
			}
			header.Set("Cookie", strings.Join(cookies, "; "))
			requestURL := &url.URL{Path: "/", RawQuery: strings.Join(query, "&")}
			makeRequest := func() *fwcontext.Request {
				raw := &http.Request{Method: http.MethodPost, URL: requestURL, Header: header, ContentLength: int64(len(scenario.body))}
				if scenario.body != "" {
					raw.Body = io.NopCloser(strings.NewReader(scenario.body))
				} else {
					raw.Body = http.NoBody
				}
				return fwcontext.MustNewRequest(raw)
			}
			request := makeRequest()
			if err := Bind(request, scenario.target); err != nil {
				b.Fatal(err)
			}
			for _, stage := range []string{"FirstBind", "RepeatBind"} {
				b.Run(stage, func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						current := request
						if stage == "FirstBind" {
							current = makeRequest()
						}
						if err := Bind(current, scenario.target); err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		})
	}
}
