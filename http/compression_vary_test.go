package http

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
)

func TestResponseVaryRetainsCompressionAndCORSOnPlainAndCompressedResponses(t *testing.T) {
	const origin = "https://client.example"
	const threshold = 256
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": true, "min_size": threshold})
	cors, err := middleware.NewCors(middleware.CorsConfig{AllowOrigins: []string{origin}, AllowMethods: []string{http.MethodGet}})
	if err != nil {
		t.Fatal(err)
	}
	mustHTTPMiddleware(t, app).Pipe(cors)
	router := mustHTTPRoute(t, app)
	for path, body := range map[string]string{
		"/small": "ok",
		"/large": strings.Repeat("example", threshold),
	} {
		if _, err := router.Get(path, func(*fwcontext.Request) *fwcontext.Response {
			return fwcontext.NewResponse().Header("Vary", "Accept-Language").Content(body)
		}); err != nil {
			t.Fatal(err)
		}
	}
	host := newTestHTTPHandler(t, app)
	for _, test := range []struct {
		name, path, accept, encoding string
	}{
		{name: "阈值以下", path: "/small", accept: "gzip"},
		{name: "客户端只接受原表示", path: "/large", accept: "identity"},
		{name: "实际压缩", path: "/large", accept: "gzip", encoding: "gzip"},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+test.path, nil)
			req.Header.Set("Origin", origin)
			req.Header.Set("Accept-Encoding", test.accept)
			recorder := httptest.NewRecorder()
			host.ServeHTTP(recorder, req)
			header := recorder.Result().Header
			if recorder.Code != http.StatusOK || header.Get("Access-Control-Allow-Origin") != origin ||
				header.Get("Content-Encoding") != test.encoding {
				t.Fatalf("请求未走预期的跨域或压缩分支: %d, %v", recorder.Code, header)
			}
			seen := make(map[string]int)
			for _, field := range header.Values("Vary") {
				for token := range strings.SplitSeq(field, ",") {
					seen[strings.ToLower(strings.TrimSpace(token))]++
				}
			}
			for _, name := range []string{"accept-encoding", "origin", "accept-language"} {
				if seen[name] != 1 {
					t.Errorf("Vary 应恰好包含一次 %q: %v", name, header.Values("Vary"))
				}
			}
		})
	}
}

func TestHeadLengthFollowsCompressionOfTheEquivalentGet(t *testing.T) {
	const threshold = 256
	const short = "中文长度"
	long := strings.Repeat(short, threshold)
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": true, "min_size": threshold})
	router := mustHTTPRoute(t, app)
	for _, test := range []struct {
		path, body, mediaType string
		noTransform           bool
	}{
		{path: "/short", body: short, mediaType: "text/plain"},
		{path: "/long", body: long, mediaType: "text/plain"},
		{path: "/binary", body: long, mediaType: "application/octet-stream"},
		{path: "/no-transform", body: long, noTransform: true},
	} {
		if _, err := router.Rule(test.path, func(*fwcontext.Request) *fwcontext.Response {
			response := fwcontext.NewResponse().Content(test.body)
			if test.mediaType != "" {
				response.Header("Content-Type", test.mediaType)
			}
			if test.noTransform {
				response.Cookie("preference", "sample").CacheControl("private, no-store, no-transform")
			}
			return response
		}, "GET|HEAD"); err != nil {
			t.Fatal(err)
		}
	}
	host := newTestHTTPHandler(t, app)
	for _, test := range []struct {
		path, accept string
		compressed   bool
		rejected     bool
	}{
		{path: "/short", accept: "gzip"},
		{path: "/short", accept: "gzip, identity;q=0", compressed: true},
		{path: "/long", accept: "gzip", compressed: true},
		{path: "/long", accept: "gzip, identity;q=0", compressed: true},
		{path: "/long", accept: "identity"},
		{path: "/binary", accept: "gzip"},
		{path: "/binary", accept: "gzip, identity;q=0", rejected: true},
		{path: "/no-transform", accept: "gzip"},
		{path: "/no-transform", accept: "gzip, identity;q=0", rejected: true},
	} {
		t.Run(test.path+"/"+test.accept, func(t *testing.T) {
			record := func(method string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(method, "http://127.0.0.1"+test.path, nil)
				req.Header.Set("Accept-Encoding", test.accept)
				recorder := httptest.NewRecorder()
				host.ServeHTTP(recorder, req)
				return recorder
			}
			get := record(http.MethodGet)
			head := record(http.MethodHead)
			assertIssue151HEAD(t, head, http.StatusOK)
			wantStatus := http.StatusOK
			if test.rejected {
				wantStatus = http.StatusNotAcceptable
			}
			if get.Code != wantStatus || (get.Header().Get("Content-Encoding") == "gzip") != test.compressed {
				t.Fatalf("GET 未得到预期表示: %d, %v", get.Code, get.Header())
			}
			if test.path == "/no-transform" && !test.rejected && (get.Header().Get("Set-Cookie") == "" ||
				get.Header().Get("Content-Encoding") != "" || get.Body.String() != long) {
				t.Fatal("含 Cookie 的 no-transform 响应应保留原正文和 Cookie")
			}
			want := strconv.Itoa(get.Body.Len())
			if test.compressed || test.rejected {
				want = ""
			}
			if got := head.Result().Header.Get("Content-Length"); got != want {
				t.Errorf("HEAD 长度为 %q，期望 %q", got, want)
			}
		})
	}
}
