package openapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// TestOpenAPIIfNoneMatch 验证条件头完整解析、弱比较、多行列表及畸形输入不会误匹配。
func TestOpenAPIIfNoneMatch(t *testing.T) {
	registry, err := NewRegistry(openapi3.Info{Title: "条件请求测试", Version: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(http.MethodGet, "/health", validOperation("health")); err != nil {
		t.Fatal(err)
	}
	handler, err := registry.Handler(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	baseline := httptest.NewRecorder()
	handler.ServeHTTP(baseline, httptest.NewRequest(http.MethodGet, "/openapi.json", nil))
	etag := baseline.Header().Get("ETag")
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, testCase := range []struct {
			name    string
			fields  []string
			matched bool
		}{
			{"无条件头", nil, false},
			{"强标签", []string{etag}, true},
			{"弱标签", []string{"W/" + etag}, true},
			{"列表", []string{`"other", W/` + etag}, true},
			{"多行", []string{`"other"`, etag}, true},
			{"多行空元素", []string{"", etag}, true},
			{"通配", []string{"*"}, true},
			{"空白", []string{" \t" + etag + "\t "}, true},
			{"通配空白", []string{"\t * "}, true},
			{"引号内逗号", []string{`"other,tag", ` + etag}, true},
			{"空列表成员", []string{", ," + etag + ","}, true},
			{"不匹配", []string{`W/"other"`}, false},
			{"空标签", []string{`""`}, false},
			{"空条件", []string{" \t,"}, false},
			{"小写弱前缀", []string{"w/" + etag}, false},
			{"弱前缀带空白", []string{"W/ " + etag}, false},
			{"匹配后非法", []string{etag + ",broken"}, false},
			{"未闭合", []string{etag + `, "broken`}, false},
			{"缺少分隔符", []string{etag + etag}, false},
			{"通配混合", []string{"*," + etag}, false},
			{"多行通配混合", []string{"*", etag}, false},
			{"标签内空格", []string{`"bad tag",` + etag}, false},
			{"标签内控制符", []string{"\"bad\x7ftag\"," + etag}, false},
		} {
			t.Run(method+"/"+testCase.name, func(t *testing.T) {
				request := httptest.NewRequest(method, "/openapi.json", nil)
				for _, field := range testCase.fields {
					request.Header.Add("If-None-Match", field)
				}
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				wantStatus := http.StatusOK
				if testCase.matched {
					wantStatus = http.StatusNotModified
				}
				if response.Code != wantStatus || response.Header().Get("ETag") != etag {
					t.Fatalf("条件请求状态=%d，预期=%d，响应头=%v", response.Code, wantStatus, response.Header())
				}
				if testCase.matched || method == http.MethodHead {
					if response.Body.Len() != 0 {
						t.Fatal("304 或 HEAD 不得输出正文")
					}
				} else if response.Body.String() != baseline.Body.String() {
					t.Fatal("未匹配请求应返回原始冻结文档")
				}
				if testCase.matched && response.Header().Get("Content-Length") != "" {
					t.Fatal("304 不应声明正文长度")
				}
			})
		}
	}
}
