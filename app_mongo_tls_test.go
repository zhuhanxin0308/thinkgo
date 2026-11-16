package framework

import (
	"errors"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

func TestProductionDatabaseProviderRejectsInsecureMongoTLSBeforeDial(t *testing.T) {
	for _, test := range []struct {
		name, environment string
		params            map[string]any
		reject            bool
	}{
		{name: "生产默认开启验证", environment: "production"},
		{name: "安全显式配置", environment: "prod", params: map[string]any{"ssl": "TRUE", "tlsInsecure": "false"}},
		{name: "关闭证书验证", environment: "production", params: map[string]any{"sslInsecure": "true"}, reject: true},
		{name: "关闭主机名验证", environment: "release", params: map[string]any{"tlsAllowInvalidHostnames": "true"}, reject: true},
		{name: "关闭 TLS", environment: "production", params: map[string]any{"tls": "false"}, reject: true},
		{name: "重复的 TLS 别名", environment: "production", params: map[string]any{"tls": "true", "ssl": "true"}, reject: true},
		{name: "关闭证书吊销检查", environment: "production", params: map[string]any{"tlsDisableCertificateRevocationCheck": "true"}, reject: true},
		{name: "本地显式覆盖", environment: "test", params: map[string]any{"tlsInsecure": "true"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			app := NewAppUninitialized(t.TempDir())
			t.Cleanup(func() { _ = app.Close() })
			app.databaseStartupPolicy = DatabaseStartupLazy
			if err := app.config.Set("app.app_env", test.environment); err != nil {
				t.Fatal(err)
			}
			configuration := map[string]any{
				"default": "primary",
				"connections": map[string]any{
					"primary": map[string]any{"type": "mongo", "hostname": "127.0.0.1", "hostport": "27017", "database": "orders", "params": test.params},
				},
			}
			if err := app.config.Set("database", configuration); err != nil {
				t.Fatal(err)
			}
			if err := (&appDatabaseProvider{}).Initialize(app); err != nil {
				t.Fatal(err)
			}
			startupErr := app.StartupError()
			if test.reject {
				if !errors.Is(startupErr, db.ErrInvalidDatabaseConfig) || !strings.Contains(startupErr.Error(), "primary") {
					t.Fatalf("生产错误应在连接前定位到命名配置: %v", startupErr)
				}
			} else if startupErr != nil {
				t.Fatalf("有效配置应通过惰性数据库启动: %v", startupErr)
			}
		})
	}
}
