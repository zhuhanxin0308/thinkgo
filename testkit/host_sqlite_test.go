//go:build cgo

package testkit

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/db"
	"github.com/zhuhanxin0308/thinkgo/v3/db/connector"
)

// TestHostOwnsConfiguredDatabase 验证默认连接别名、真实数据库请求和关闭所有权。
func TestHostOwnsConfiguredDatabase(t *testing.T) {
	connector.RegisterBuiltins()
	database, err := db.Connect(db.Config{Type: "sqlite", Database: filepath.Join(t.TempDir(), "test.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.Execute("CREATE TABLE items (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Execute("INSERT INTO items (id) VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	host := New(t, Options{
		Database: database,
		Config:   map[string]any{"database.default": "testing"},
		Register: func(app *framework.App) error {
			return app.RegisterRouteLoader(func(app *framework.App) error {
				app.Route().Get("/items", func() (map[string]any, error) {
					count, err := app.DB().Table("items").Count()
					return map[string]any{"total": count}, err
				})
				return nil
			})
		},
	})
	manager, err := framework.ResolveServiceAs[*db.Manager](host.App(), framework.ServiceDBManager)
	if err != nil {
		t.Fatal(err)
	}
	if connection, err := manager.Default(); err != nil || connection != database {
		t.Fatalf("默认连接不一致: %p %v", connection, err)
	}
	response, err := host.JSON(http.MethodGet, "/items", nil)
	if err != nil || response.Code != http.StatusOK || response.Body.String() != `{"total":1}` {
		t.Fatalf("真实数据库请求失败: %#v %v", response, err)
	}
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Table("items").Count(); err == nil {
		t.Fatal("宿主关闭后仍可访问已拥有的连接")
	}
}
