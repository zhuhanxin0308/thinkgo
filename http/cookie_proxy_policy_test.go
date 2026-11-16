package http

import (
	stdhttp "net/http"
	"net/http/httptest"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/cookie"
)

func TestCookieFactoryUsesVerifiedProxyProtocolThroughHTTPKernel(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]any{"enable": false})
	mustHTTPConfig(t, app).Set("app.server.trusted_proxies", []string{"10.23.0.0/24"})
	factory, err := cookie.NewCookie(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = mustHTTPRoute(t, app).Get("/preference", func(req *fwcontext.Request) *fwcontext.Response {
		response := fwcontext.NewResponse().Content("ok")
		current, err := factory.ForRequestWithSecure(req.Raw(), nil, req.IsSsl())
		if err != nil {
			t.Error(err)
			return response.Code(stdhttp.StatusInternalServerError)
		}
		header, err := current.BuildHeader("language", "zh")
		if err != nil {
			t.Error(err)
			return response.Code(stdhttp.StatusInternalServerError)
		}
		return response.AddHeader("Set-Cookie", header)
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := newTestHTTPHandler(t, app)
	for _, test := range []struct {
		name, scheme, source, forwarded string
		secure                          bool
	}{
		{"直连TLS", "https", "192.0.2.1:1234", "", true},
		{"可信非回环代理HTTPS", "http", "10.23.0.2:1234", "https", true},
		{"可信代理HTTP", "http", "10.23.0.2:1234", "http", false},
		{"不可信来源伪造", "http", "192.0.2.1:1234", "https", false},
		{"客户端前置伪造", "http", "10.23.0.2:1234", "https, http", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := httptest.NewRequest(stdhttp.MethodGet, test.scheme+"://example.test/preference", nil)
			raw.RemoteAddr = test.source
			raw.Header.Set("X-Forwarded-Proto", test.forwarded)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, raw)
			cookies := recorder.Result().Cookies()
			if recorder.Code != stdhttp.StatusOK || len(cookies) != 1 ||
				cookies[0].Name != "language" || cookies[0].Value != "zh" || cookies[0].Secure != test.secure {
				t.Fatalf("可信协议未传递至实际 Cookie: status=%d cookies=%#v", recorder.Code, cookies)
			}
		})
	}
}
