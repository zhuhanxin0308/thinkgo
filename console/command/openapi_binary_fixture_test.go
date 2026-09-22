package command

// commentBinaryFixture 在独立程序内验证真实路径前缀、绑定、JSON 响应、字段说明和 OpenAPI 校验。
const commentBinaryFixture = `package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/zhuhanxin0308/thinkgo/framework"
	fwhttp "github.com/zhuhanxin0308/thinkgo/framework/http"
	"example.com/project/app/index/api"
	"example.com/project/internal/apidoc"
)

func main() {
	if err := verify(); err != nil { panic(err) }
	fmt.Println("源码注释验收通过")
}

func verify() error {
	registry, err := apidoc.NewRegistry(openapi3.Info{Title: "用户接口", Version: "1"})
	if err != nil { return err }
	base, err := os.MkdirTemp("", "thinkgo-comment-runtime-")
	if err != nil { return err }
	defer os.RemoveAll(base)
	if err := os.MkdirAll(filepath.Join(base, "config"), 0o700); err != nil { return err }
	for name, content := range map[string]string{
		"app": ` + "`" + `{"session_enable":false,"server":{"host":"127.0.0.1","port":8080,"allowed_hosts":["example.com"]},"compression":{"enable":false}}` + "`" + `,
		"cache": ` + "`" + `{"default":"memory","stores":{"memory":{"type":"memory"}}}` + "`" + `,
		"log": ` + "`" + `{"close":true,"default":"file","channels":{"file":{"type":"file","path":"runtime/log"}}}` + "`" + `,
		"cookie": "{}", "session": ` + "`" + `{"type":"memory","name":"TESTSESSID","expire":600}` + "`" + `,
	} {
		if err := os.WriteFile(filepath.Join(base, "config", name+".json"), []byte(content), 0o600); err != nil { return err }
	}
	app := framework.NewConsoleAppUninitialized(base)
	defer app.Close()
	if err := app.RegisterRouteLoader(func(app *framework.App) error {
		var registrationErr error
		app.Route().Group("api", func() {
			registrationErr = registry.Routes(app.Route()).Get("/users/:id", api.Show)
		})
		return registrationErr
	}); err != nil { return err }
	if err := app.Initialize(); err != nil { return err }
	if err := app.EnsureReady(); err != nil { return err }
	if err := app.LoadRoutes(); err != nil { return err }
	document, err := registry.JSON(context.Background())
	if err != nil { return err }
	for _, text := range []string{"/api/users/{id}", "查询用户。", "用户编号。", "用户姓名。", "公开资料"} {
		if !strings.Contains(string(document), text) { return fmt.Errorf("文档缺少 %s: %s", text, document) }
	}
	handler, err := fwhttp.NewHttp(app)
	if err != nil { return err }
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/users/1", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Ada") { return fmt.Errorf("实际响应错误: %d %s", response.Code, response.Body.String()) }
	invalid := httptest.NewRecorder()
	handler.ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/api/users/0", nil))
	if invalid.Code != http.StatusUnprocessableEntity { return fmt.Errorf("验证规则失效: %d %s", invalid.Code, invalid.Body.String()) }
	return nil
}
`
