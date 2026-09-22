package command

// crudBusinessTestSource 在生成的独立应用内执行真实请求，覆盖数据白名单及事务边界。
const crudBusinessTestSource = `//go:build cgo

package api

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/zhuhanxin0308/thinkgo/framework"
	"github.com/zhuhanxin0308/thinkgo/framework/db"
	"github.com/zhuhanxin0308/thinkgo/framework/db/connector"
	"github.com/zhuhanxin0308/thinkgo/framework/openapi"
	"github.com/zhuhanxin0308/thinkgo/framework/testkit"
	model "example.com/project/app/index/model"
)

// TestGeneratedBusinessAPI 验证生成接口与实际数据库的完整往返。
func TestGeneratedBusinessAPI(t *testing.T) {
	connector.RegisterBuiltins()
	database, err := db.Connect(db.Config{Type: "sqlite", Database: filepath.Join(t.TempDir(), "api.sqlite"), MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.Execute("CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, active BOOLEAN NOT NULL, note TEXT NULL, secret TEXT NOT NULL DEFAULT 'private', count INTEGER DEFAULT 0)"); err != nil { t.Fatal(err) }
	host := testkit.New(t, testkit.Options{Database: database, Register: func(app *framework.App) error {
		if err := app.RegisterModel("User", &model.User{}); err != nil { return err }
		return app.RegisterRouteLoader(func(app *framework.App) error {
			registry, err := openapi.NewRegistry(openapi3.Info{Title: "业务验收", Version: "1"})
			if err != nil { return err }
			return RegisterUserRoutes(app.Route(), registry)
		})
	}})
	create, err := host.JSON(http.MethodPost, "/users", map[string]any{"name":"Ada", "note":"memo"})
	if err != nil || create.Code != http.StatusCreated { t.Fatalf("创建失败: %#v %v", create, err) }
	created, err := testkit.DecodeJSON[UserOutput](create)
	if err != nil || created.ID != 1 || !created.Active || created.Note == nil { t.Fatalf("创建数据错误: %#v %v", created, err) }
	secret, err := database.Table("users").Where("id", 1).Value("secret")
	if err != nil || secret != "private" { t.Fatalf("未选择字段的数据库默认值被覆盖: %#v %v", secret, err) }
	update, err := host.JSON(http.MethodPatch, "/users/1", map[string]any{"active":false,"note":nil})
	if err != nil || update.Code != http.StatusOK { t.Fatalf("更新失败: %#v %v", update, err) }
	updated, err := testkit.DecodeJSON[UserOutput](update)
	if err != nil || updated.Active || updated.Note != nil || updated.Name != "Ada" { t.Fatalf("PATCH 语义错误: %#v %v", updated, err) }
	for _, test := range []struct { method, path string; body any; status int }{
		{http.MethodPost, "/users", map[string]any{"name":"x"}, http.StatusUnprocessableEntity},
		{http.MethodPost, "/users", map[string]any{"name":"Ada","secret":"leak"}, http.StatusBadRequest},
		{http.MethodPatch, "/users/1", map[string]any{}, http.StatusUnprocessableEntity},
		{http.MethodPatch, "/users/1", map[string]any{"name":nil}, http.StatusUnprocessableEntity},
		{http.MethodPatch, "/users/1", map[string]any{"id":2}, http.StatusBadRequest},
		{http.MethodGet, "/users/999", nil, http.StatusNotFound},
		{http.MethodGet, "/users?page_size=101", nil, http.StatusUnprocessableEntity},
	} {
		response, err := host.JSON(test.method, test.path, test.body)
		if err != nil || response.Code != test.status { t.Fatalf("边界请求失败: %#v %#v %v", test, response, err) }
	}
	pageResponse, err := host.JSON(http.MethodGet, "/users?page_size=1", nil)
	if err != nil || pageResponse.Code != http.StatusOK { t.Fatalf("列表失败: %#v %v", pageResponse, err) }
	page, err := testkit.DecodeJSON[db.Paginator[UserOutput]](pageResponse)
	if err != nil || page.Total != 1 || len(page.List) != 1 || strings.Contains(pageResponse.Body.String(), "secret") { t.Fatalf("分页或输出白名单错误: %#v %v", page, err) }
	// 插入后让再次读取必然失败，验证创建和响应读取共用事务。
	if _, err := database.Execute("CREATE TRIGGER invalidate_after_insert AFTER INSERT ON users WHEN NEW.name = 'Rollback' BEGIN UPDATE users SET active = 'invalid' WHERE id = NEW.id; END"); err != nil { t.Fatal(err) }
	failed, err := host.JSON(http.MethodPost, "/users", map[string]any{"name":"Rollback"})
	if err != nil || failed.Code != http.StatusInternalServerError { t.Fatalf("事务故障未报告: %#v %v", failed, err) }
	if count, err := database.Table("users").Count(); err != nil || count != 1 { t.Fatalf("失败创建未回滚: %d %v", count, err) }
	if _, err := database.Execute("CREATE TRIGGER invalidate_after_update AFTER UPDATE ON users WHEN NEW.name = 'RollbackUpdate' BEGIN UPDATE users SET active = 'invalid' WHERE id = NEW.id; END"); err != nil { t.Fatal(err) }
	failedUpdate, err := host.JSON(http.MethodPatch, "/users/1", map[string]any{"name":"RollbackUpdate"})
	if err != nil || failedUpdate.Code != http.StatusInternalServerError { t.Fatalf("更新事务故障未报告: %#v %v", failedUpdate, err) }
	unchanged, err := host.JSON(http.MethodGet, "/users/1", nil)
	if err != nil || unchanged.Code != http.StatusOK { t.Fatalf("失败更新没有回滚: %#v %v", unchanged, err) }
	preserved, err := testkit.DecodeJSON[UserOutput](unchanged)
	if err != nil || preserved.Name != "Ada" || preserved.Active { t.Fatalf("失败更新改变了记录: %#v %v", preserved, err) }
	deleted, err := host.JSON(http.MethodDelete, "/users/1", nil)
	if err != nil || deleted.Code != http.StatusNoContent || deleted.Body.Len() != 0 { t.Fatalf("删除响应错误: %#v %v", deleted, err) }
	missing, err := host.JSON(http.MethodGet, "/users/1", nil)
	if err != nil || missing.Code != http.StatusNotFound { t.Fatalf("删除未生效: %#v %v", missing, err) }
}
`
