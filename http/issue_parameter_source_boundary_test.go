package http

import (
	"bytes"
	"mime/multipart"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

// TestIssue29AuthorizationUsesOneRouteID 通过实际 HTTP 内核验证文档中的授权/写入数据流。
// 这里的权限关系是测试夹具，不是框架提供的登录或对象级权限实现。
func TestIssue29AuthorizationUsesOneRouteID(t *testing.T) {
	var multipartBody bytes.Buffer
	writer := multipart.NewWriter(&multipartBody)
	for key, value := range map[string]string{"id": "456", "name": "changed"} {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, routeID, query, contentType, body, merged string
		status                                          int
	}{
		{"json", "123", "789", "application/json", `{"id":"456","name":"changed"}`, "456", 200},
		{"form", "123", "789", "application/x-www-form-urlencoded", "id=456&name=changed", "456", 200},
		{"multipart", "123", "789", writer.FormDataContentType(), multipartBody.String(), "456", 200},
		{"query", "123", "456", "application/x-www-form-urlencoded", "name=changed", "456", 200},
		{"empty_body_id", "123", "456", "application/json", `{"id":"","name":"changed"}`, "", 200},
		{"denied_route", "456", "123", "application/json", `{"id":"123","name":"changed"}`, "123", 404},
	} {
		t.Run(test.name, func(t *testing.T) {
			app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
			records := map[string]string{"123": "own", "456": "other"}
			called, merged := false, ""
			_, err := mustHTTPRoute(t, app).Post("/records/:id", func(request *fwcontext.Request) *fwcontext.Response {
				called, merged = true, request.Param("id")
				id := request.Route("id")
				if id != "123" { // 权限夹具：当前已认证主体仅拥有记录123。
					return fwcontext.NewResponse().Code(stdhttp.StatusNotFound).Content("not found")
				}
				records[id] = request.Post("name")
				return fwcontext.NewResponse().Content(id)
			})
			if err != nil {
				t.Fatal(err)
			}
			raw := httptest.NewRequest(stdhttp.MethodPost, "http://example.com/records/"+test.routeID+"?id="+test.query, strings.NewReader(test.body))
			raw.Header.Set("Content-Type", test.contentType)
			recorder := httptest.NewRecorder()
			newTestHTTPHandler(t, app).ServeHTTP(recorder, raw)
			if !called || recorder.Code != test.status || merged != test.merged {
				t.Fatalf("dispatch/source contract: called=%v status=%d merged=%q body=%q", called, recorder.Code, merged, recorder.Body.String())
			}
			wantOwn := "own"
			if test.status == stdhttp.StatusOK {
				wantOwn = "changed"
				if recorder.Body.String() != "123" {
					t.Fatalf("wrong authorized target: %q", recorder.Body.String())
				}
			}
			if records["123"] != wantOwn || records["456"] != "other" {
				t.Fatalf("parameter pollution changed an unauthorized object: %#v", records)
			}
		})
	}
}
