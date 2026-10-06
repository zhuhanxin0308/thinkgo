package framework

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/config"
	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/route"
)

func TestIssue17RouteConfigurationDefaultsToMustRoute(t *testing.T) {
	for _, tc := range []struct {
		name                string
		values              map[string]interface{}
		wantAuto, wantError bool
	}{
		{name: "missing"},
		{name: "empty", values: map[string]interface{}{}},
		{name: "unrelated_option", values: map[string]interface{}{"url_html_suffix": "html"}},
		{name: "explicit_must", values: map[string]interface{}{"url_route_must": true}},
		{name: "explicit_auto", values: map[string]interface{}{"url_route_must": false}, wantAuto: true},
		{name: "text_true", values: map[string]interface{}{"url_route_must": "true"}},
		{name: "text_false", values: map[string]interface{}{"url_route_must": "false"}, wantAuto: true},
		{name: "invalid_bool", values: map[string]interface{}{"url_route_must": "invalid"}, wantError: true},
		{name: "null", values: map[string]interface{}{"url_route_must": nil}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := &App{config: config.NewConfig(), route: route.NewRouter()}
			if tc.values != nil {
				if err := app.config.Set("route", tc.values); err != nil {
					t.Fatal(err)
				}
			}
			app.applyRouteConfig()
			if got := app.StartupError(); (got != nil) != tc.wantError {
				t.Fatalf("startup error=%v wantError=%v", got, tc.wantError)
			}
			if got := app.route.AutoRouteEnabled(); got != tc.wantAuto {
				t.Errorf("automatic dispatch=%v want=%v", got, tc.wantAuto)
			}
			request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/user/delete", nil))
			matched, _, err := app.route.Match(request)
			if err != nil || (matched != nil) != tc.wantAuto {
				t.Fatalf("route=%v err=%v wantAuto=%v", matched, err, tc.wantAuto)
			}
			if tc.wantAuto && (!matched.IsAuto() || matched.Handler() != "user/delete") {
				t.Fatal("explicit opt-in lost its dispatch behavior")
			}
		})
	}
}
