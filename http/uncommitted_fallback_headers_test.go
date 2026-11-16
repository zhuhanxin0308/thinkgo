package http

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

func TestUncommittedStreamFallbackDropsFailedBusinessHeaders(t *testing.T) {
	for _, compression := range []bool{false, true} {
		t.Run(map[bool]string{false: "identity", true: "gzip"}[compression], func(t *testing.T) {
			app := newTestHTTPApp(t, t.TempDir(), map[string]any{"enable": compression, "min_size": 1})
			if _, err := mustHTTPRoute(t, app).Get("/download", func(*fwcontext.Request) *fwcontext.Response {
				return fwcontext.NewResponse().
					Header("Set-Cookie", "failed_stream=staged").
					Header("X-Failed-Stream", "staged").
					Header("Content-Disposition", "attachment; filename=fixture.txt").
					Header("Location", "/unexpected").
					Stream(func(io.Writer) error { return errors.New("流打开失败") })
			}); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/download", nil)
			req.Header.Set("Accept-Encoding", "gzip")
			recorder := httptest.NewRecorder()
			newTestHTTPHandler(t, app).ServeHTTP(recorder, req)
			header := recorder.Result().Header
			for _, field := range []string{"Set-Cookie", "Content-Disposition", "Location", "X-Failed-Stream"} {
				if value := header.Get(field); value != "" {
					t.Errorf("降级响应保留了业务头 %s=%q", field, value)
				}
			}
			body := recorder.Body.Bytes()
			if encoding := header.Get("Content-Encoding"); encoding != "" {
				var err error
				body, err = decodeCompressedTestBody(encoding, body)
				if err != nil {
					t.Fatal(err)
				}
			}
			if recorder.Code != http.StatusInternalServerError || header.Get("Content-Type") != "text/plain; charset=utf-8" || string(body) != http.StatusText(http.StatusInternalServerError) {
				t.Fatalf("降级响应错误: status=%d body=%q headers=%v", recorder.Code, body, header)
			}
			if compression && header.Get("Vary") != "Accept-Encoding" {
				t.Fatalf("压缩降级应保留协商维度: %v", header)
			}
		})
	}
}
