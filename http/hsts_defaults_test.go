package http

import (
	stdhttp "net/http"
	"net/http/httptest"
	"testing"

	framework "github.com/zhuhanxin0308/thinkgo/v3"
	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

func TestProductionBrowserHSTSRequiresVerifiedHTTPS(t *testing.T) {
	const policy = "max-age=31536000"
	for _, deployment := range []struct {
		name, environment string
		profile           framework.SecurityProfile
		wantSecure        string
	}{
		{name: "生产浏览器", environment: "prod", profile: framework.SecurityProfileBrowserCookie, wantSecure: policy},
		{name: "非生产浏览器", environment: "test", profile: framework.SecurityProfileBrowserCookie},
		{name: "生产无状态", environment: "production", profile: framework.SecurityProfileStatelessAPI},
	} {
		t.Run(deployment.name, func(t *testing.T) {
			app := newRateLimitWarningApp(t, deployment.environment, deployment.profile, nil)
			if err := app.Config().Set("app.server.trusted_proxies", []any{"192.0.2.1/32"}); err != nil {
				t.Fatal(err)
			}
			app.Route().Get("/hsts", func(*fwcontext.Request) *fwcontext.Response { return fwcontext.NewResponse().Content("ok") })
			server, err := NewHttp(app)
			if err != nil {
				t.Fatal(err)
			}
			for _, request := range []struct {
				name, scheme, peer, forwarded string
				verified                      bool
			}{
				{name: "直连 TLS", scheme: "https", peer: "198.51.100.2:1234", verified: true},
				{name: "可信代理", scheme: "http", peer: "192.0.2.1:1234", forwarded: "https", verified: true},
				{name: "伪造代理头", scheme: "http", peer: "198.51.100.2:1234", forwarded: "https"},
				{name: "明文请求", scheme: "http", peer: "192.0.2.1:1234"},
			} {
				t.Run(request.name, func(t *testing.T) {
					raw := httptest.NewRequest(stdhttp.MethodGet, request.scheme+"://example.com/hsts", nil)
					raw.RemoteAddr = request.peer
					raw.Header.Set("X-Forwarded-Proto", request.forwarded)
					writer := httptest.NewRecorder()
					server.ServeHTTP(writer, raw)
					want := ""
					if request.verified {
						want = deployment.wantSecure
					}
					if got := writer.Result().Header.Get("Strict-Transport-Security"); writer.Code != stdhttp.StatusOK || got != want {
						t.Fatalf("HTTP %d 的 HSTS 为 %q，期望 %q", writer.Code, got, want)
					}
				})
			}
		})
	}
}
