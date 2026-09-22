package deploy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework"
	frameworkcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
	"github.com/zhuhanxin0308/thinkgo/framework/route"
	"github.com/zhuhanxin0308/thinkgo/framework/testkit"
)

// TestAutomaticRouteAuditUsesLoadedRouter 验证部署门禁读取加载器执行后的实际策略，
// 严格模式阻断自动调度，但审计不会改变默认行为或覆盖业务显式策略。
func TestAutomaticRouteAuditUsesLoadedRouter(t *testing.T) {
	for _, test := range []struct {
		name       string
		mustRoute  *bool
		override   *bool
		wantAuto   bool
		wantLevel  Level
		wantStrict bool
	}{
		{name: "default", wantAuto: true, wantLevel: LevelWarn, wantStrict: true},
		{name: "explicit-routes", mustRoute: deployBool(true), wantLevel: LevelPass},
		{name: "loader-enables-auto", mustRoute: deployBool(true), override: deployBool(true), wantAuto: true, wantLevel: LevelWarn, wantStrict: true},
		{name: "loader-disables-auto", mustRoute: deployBool(false), override: deployBool(false), wantLevel: LevelPass},
	} {
		t.Run(test.name, func(t *testing.T) {
			routeConfig := map[string]any{}
			if test.mustRoute != nil {
				routeConfig["url_route_must"] = *test.mustRoute
			}
			host := testkit.New(t, testkit.Options{
				Config: map[string]any{
					"app.app_env": "production", "app.security_profile": string(framework.SecurityProfileStatelessAPI),
					"app.database_startup_policy": string(framework.DatabaseStartupDisabled), "route": routeConfig,
				},
				Register: func(app *framework.App) error {
					if test.override == nil {
						return nil
					}
					return app.RegisterRouteLoader(func(current *framework.App) error {
						router, err := framework.ResolveServiceAs[*route.Router](current, framework.ServiceRoute)
						if err != nil {
							return err
						}
						return router.EnableAutoRoute(*test.override)
					})
				},
			})
			app := host.App()
			result := automaticRouteAudit(t).Run(context.Background(), app)
			if result.Level != test.wantLevel {
				t.Fatalf("自动路由审计状态错误: got=%#v want=%s", result, test.wantLevel)
			}
			report := Report{Results: []Result{result}}
			if report.Failed(false) || report.Failed(true) != test.wantStrict {
				t.Fatalf("普通与严格模式门禁错误: %#v", report)
			}
			request := frameworkcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "/auditcontroller/index", nil))
			matched, _, err := app.Route().Match(request)
			if err != nil || (matched != nil && matched.IsAuto()) != test.wantAuto {
				t.Fatalf("审计改变了实际自动路由行为: matched=%v err=%v", matched, err)
			}
		})
	}
}

// TestAutomaticRouteAuditFailsWhenRoutesCannotLoad 验证加载失败不会被报告为安全的显式路由。
func TestAutomaticRouteAuditFailsWhenRoutesCannotLoad(t *testing.T) {
	check := automaticRouteAudit(t)
	if result := check.Run(context.Background(), nil); result.Level != LevelFail {
		t.Fatalf("空应用必须失败: %#v", result)
	}
	app, _ := newDeployTestApp(t)
	if err := app.RegisterRouteLoader(func(*framework.App) error { return errors.New("路由加载失败") }); err != nil {
		t.Fatal(err)
	}
	if result := check.Run(context.Background(), app); result.Level != LevelFail {
		t.Fatalf("路由加载失败必须阻断: %#v", result)
	}
}

func automaticRouteAudit(t *testing.T) Check {
	t.Helper()
	for _, check := range NewDefaultAuditor().checks {
		if check.Name() == "routes.automatic_dispatch" {
			return check
		}
	}
	t.Fatal("默认发布审计缺少自动路由门禁")
	return nil
}

func deployBool(value bool) *bool { return &value }
