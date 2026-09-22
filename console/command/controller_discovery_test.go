package command

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework"
	"github.com/zhuhanxin0308/thinkgo/framework/console"
)

// TestRefreshControllerDiscoveryGeneratesApplicationAssembly 验证一个业务应用的
// 控制器、模型、验证器和路由进入自己的静态装配入口，根 app 不承载业务层。
func TestRefreshControllerDiscoveryGeneratesApplicationAssembly(t *testing.T) {
	basePath := t.TempDir()
	writeDiscoveryModuleFixture(t, basePath, "example.com/project")
	controllerPath := filepath.Join(basePath, "app", "index", "controller")
	for name, source := range map[string]string{
		"base_controller.go": "package controller\n\ntype BaseController struct{}\n",
		"index.go":           "package controller\n\ntype Index struct{}\n",
		"user.go":            "package controller\n\ntype User struct{}\ntype userInput struct{}\n",
	} {
		writeDiscoveryFixture(t, basePath, filepath.ToSlash(filepath.Join("app", "index", "controller", name)), source)
	}
	writeDiscoveryFixture(t, basePath, "app/index/validate/user.go", "package validate\n\ntype User struct{}\n")
	writeDiscoveryFixture(t, basePath, "app/index/model/user.go", "package model\n\ntype User struct { ID int64 }\n")
	writeDiscoveryFixture(t, basePath, "app/index/route/app.go", `package route

import framework "github.com/zhuhanxin0308/thinkgo/framework"

func Load(route *framework.Route) {}
`)

	application := &framework.App{BasePath: basePath, ApplicationPath: filepath.Join(basePath, "app")}
	if err := RefreshControllerDiscovery(application); err != nil {
		t.Fatalf("刷新控制器发现文件失败: %v", err)
	}
	generatedPath := filepath.Join(basePath, "app", "index", applicationDiscoveryFilename)
	generated, err := os.ReadFile(generatedPath)
	if err != nil {
		t.Fatalf("读取控制器发现文件失败: %v", err)
	}
	text := string(generated)
	if !strings.Contains(text, `applicationController.Index`) || !strings.Contains(text, `applicationController.User`) || strings.Contains(text, "userInput") || strings.Contains(text, `applicationController.BaseController`) {
		t.Fatalf("控制器装配结果错误:\n%s", text)
	}
	if strings.Index(text, `applicationController.Index`) > strings.Index(text, `applicationController.User`) {
		t.Fatalf("控制器发现结果必须稳定排序:\n%s", text)
	}
	if !strings.Contains(text, `applicationValidate.User`) || !strings.Contains(text, `ValidatorFactories`) {
		t.Fatalf("验证器未进入应用装配入口:\n%s", text)
	}
	if !strings.Contains(text, `applicationModel.User`) || !strings.Contains(text, `Models: map[string]interface{}`) {
		t.Fatalf("模型未进入应用装配入口:\n%s", text)
	}
	if !strings.Contains(text, `applicationRoute.Load`) {
		t.Fatalf("app/index/route 包未进入应用装配入口:\n%s", text)
	}
	rootGenerated := readGeneratedDiscoverySource(t, basePath, "app/autoload_generated.go")
	if !strings.Contains(rootGenerated, "RegisterApplications") || !strings.Contains(rootGenerated, "applicationIndex.Definition()") {
		t.Fatalf("根应用清单未引用 index 定义:\n%s", rootGenerated)
	}
	for _, obsolete := range []string{filepath.Join(basePath, "app", "application.go"), filepath.Join(controllerPath, applicationDiscoveryFilename), filepath.Join(basePath, "internal")} {
		if _, err := os.Stat(obsolete); !os.IsNotExist(err) {
			t.Fatalf("控制器发现不得创建旧架构路径 %q: %v", obsolete, err)
		}
	}
}

// TestGeneratedApplicationAssemblyCompilesAndResolvesValidator 验证复制到独立宿主
// 后，生成的模型和原生多应用入口无需手写注册表即可编译并自动解析。
func TestGeneratedApplicationAssemblyCompilesAndResolvesValidator(t *testing.T) {
	basePath := t.TempDir()
	writeDiscoveryModuleFixture(t, basePath, "example.com/downstream")
	writeDiscoveryFixture(t, basePath, "app/service.go", "package app\n\nfunc Services() []interface{} { return nil }\n")
	writeDiscoveryFixture(t, basePath, "app/provider.go", "package app\n\nfunc Providers() map[string]interface{} { return nil }\n")
	writeDiscoveryFixture(t, basePath, "app/event.go", `package app

import framework "github.com/zhuhanxin0308/thinkgo/framework"

func Events() framework.EventDefinition { return framework.EventDefinition{} }
`)
	writeDiscoveryFixture(t, basePath, "app/middleware.go", `package app

import "github.com/zhuhanxin0308/thinkgo/framework/middleware"

func Middleware() []middleware.Handler { return nil }
`)
	writeDiscoveryFixture(t, basePath, "app/index/event.go", `package index

import framework "github.com/zhuhanxin0308/thinkgo/framework"

func Events() framework.EventDefinition { return framework.EventDefinition{} }
`)
	writeDiscoveryFixture(t, basePath, "app/index/middleware.go", `package index

import "github.com/zhuhanxin0308/thinkgo/framework/middleware"

func Middleware() []middleware.Handler { return nil }
`)
	writeDiscoveryFixture(t, basePath, "app/index/provider.go", "package index\n\nfunc Providers() map[string]interface{} { return nil }\n")
	writeDiscoveryFixture(t, basePath, "app/index/controller/index.go", "package controller\n\ntype Index struct{}\n")
	writeDiscoveryFixture(t, basePath, "app/index/validate/user.go", `package validate

import frameworkvalidate "github.com/zhuhanxin0308/thinkgo/framework/validate"

type User struct { frameworkvalidate.Validator }

func NewUser() *User {
	validator := &User{}
	validator.SetRules(map[string]string{"name": "required"})
	return validator
}
`)
	writeDiscoveryFixture(t, basePath, "app/index/route/app.go", `package route

import framework "github.com/zhuhanxin0308/thinkgo/framework"

func Load(route *framework.Route) {}
`)
	writeDiscoveryFixture(t, basePath, "config/app.json", `{"default_timezone":"Asia/Shanghai","with_route":true,"default_app":"index"}`)
	writeDiscoveryFixture(t, basePath, "config/log.json", `{"default":"file","level":[],"type_channel":{},"close":true,"processor":null,"channels":{}}`)
	writeDiscoveryFixture(t, basePath, "config/cache.json", `{"default":"memory","stores":{"memory":{"type":"memory","max_entries":128,"prefix":"","expire":0,"tag_prefix":"tag:","serialize":[]}}}`)
	application := &framework.App{BasePath: basePath, ApplicationPath: filepath.Join(basePath, "app")}
	modelCommand := &MakeModel{}
	modelCommand.SetApp(application)
	if err := modelCommand.Execute(&console.Input{Args: []string{"User"}}, generatorTestOutput()); err != nil {
		t.Fatalf("生成独立宿主模型失败: %v", err)
	}
	if err := RefreshControllerDiscovery(application); err != nil {
		t.Fatalf("生成独立宿主装配入口失败: %v", err)
	}
	writeDiscoveryFixture(t, basePath, "app/autoload_generated_test.go", `package app

import (
	"context"
	"errors"
	"testing"

	framework "github.com/zhuhanxin0308/thinkgo/framework"
	"github.com/zhuhanxin0308/thinkgo/framework/db"
	applicationModel "example.com/downstream/app/index/model"
	applicationValidate "example.com/downstream/app/index/validate"
)

// generatedModelConnection 检查生成模型是否使用宿主绑定的数据库与默认表名。
type generatedModelConnection struct { identity db.ConnectionID }

func (connection *generatedModelConnection) ConnectionID() db.ConnectionID { return connection.identity }
func (connection *generatedModelConnection) Select(ctx context.Context, request db.SelectRequest) ([]map[string]interface{}, error) {
	if err := ctx.Err(); err != nil { return nil, err }
	if request.Table() != "user" { return nil, errors.New("生成模型未使用默认表名") }
	return []map[string]interface{}{{"id": int64(7)}}, nil
}
func (*generatedModelConnection) Insert(context.Context, db.InsertRequest) (db.InsertResult, error) { return db.InsertResult{}, errors.New("测试不允许插入") }
func (*generatedModelConnection) Update(context.Context, db.UpdateRequest) (db.UpdateResult, error) { return db.UpdateResult{}, errors.New("测试不允许更新") }
func (*generatedModelConnection) Delete(context.Context, db.DeleteRequest) (db.DeleteResult, error) { return db.DeleteResult{}, errors.New("测试不允许删除") }
func (*generatedModelConnection) Count(context.Context, db.CountRequest) (int64, error) { return 0, errors.New("测试不允许计数") }
func (*generatedModelConnection) Close() error { return nil }

func TestGeneratedValidatorBinding(t *testing.T) {
	application := framework.NewAppUninitialized("..")
	defer application.Close()
	if err := Register(application); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := application.Initialize(); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	resolved, err := application.Make(application.ParseClass("validate", "User"))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	validator, ok := resolved.(*applicationValidate.User)
	if !ok {
		t.Fatalf("type: %T", resolved)
	}
	result, err := validator.Validate(map[string]interface{}{"name": "ThinkPHP"})
	if err != nil || !result.Valid() {
		t.Fatalf("validate: result=%#v err=%v", result, err)
	}
	database := db.NewDB(&generatedModelConnection{identity: db.NewConnectionID("generated-model")})
	defer database.Close()
	if err := application.Instance(string(framework.ServiceDB), database); err != nil {
		t.Fatalf("绑定宿主数据库失败: %v", err)
	}
	model, err := framework.ResolveModel[*applicationModel.User](application)
	if err != nil || model == nil || model.Model == nil {
		t.Fatalf("生成模型必须无需调用构造函数即可解析: model=%v err=%v", model, err)
	}
	row, err := model.FindMap()
	if err != nil || row["id"] != int64(7) {
		t.Fatalf("自动解析的生成模型查询失败: row=%#v err=%v", row, err)
	}
	explicit, err := applicationModel.NewUser(database)
	if err != nil || explicit == nil || explicit.Model == nil || explicit.Model == model.Model {
		t.Fatalf("便捷构造函数必须创建独立模型: model=%v err=%v", explicit, err)
	}
}
`)

	command := exec.Command("go", "test", "-mod=mod", "./...", "-count=1")
	command.Dir = basePath
	command.Env = append(os.Environ(), "GOWORK=off")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("独立宿主编译或验证器解析失败: %v\n%s", err, output)
	}
}

func writeDiscoveryFixture(t *testing.T, basePath, relativePath, content string) {
	t.Helper()
	path := filepath.Join(basePath, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建发现测试目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写入发现测试文件 %q 失败: %v", relativePath, err)
	}
}

// writeDiscoveryModuleFixture 创建具备真实框架依赖及完整校验和的只读下游模块，
// 保证语义发现通过目标项目自己的 go.mod 解析框架类型。
func writeDiscoveryModuleFixture(t *testing.T, basePath, modulePath string) {
	t.Helper()
	frameworkPath, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("解析框架模块路径失败: %v", err)
	}
	moduleFile := fmt.Sprintf(`module %s

go 1.26.6

require github.com/zhuhanxin0308/thinkgo/framework v1.0.0

replace github.com/zhuhanxin0308/thinkgo/framework => %s
`, modulePath, filepath.ToSlash(frameworkPath))
	writeDiscoveryFixture(t, basePath, "go.mod", moduleFile)
	frameworkSum, err := os.ReadFile(filepath.Join(frameworkPath, "go.sum"))
	if err != nil {
		t.Fatalf("读取框架依赖校验和失败: %v", err)
	}
	writeDiscoveryFixture(t, basePath, "go.sum", string(frameworkSum))
}

// TestServiceDiscoverCommandMatchesThinkPHPCommandName 验证开发者可使用 ThinkPHP
// 同名命令刷新编译发现信息。
func TestServiceDiscoverCommandMatchesThinkPHPCommandName(t *testing.T) {
	command := &ServiceDiscover{}
	command.Configure()
	if command.GetSignature() != "service:discover" {
		t.Fatalf("服务发现命令名称错误: %q", command.GetSignature())
	}
	if err := command.Execute(console.NewInput(), generatorTestOutput()); !strings.Contains(err.Error(), "应用实例不能为空") {
		t.Fatalf("缺少 App 时应返回明确错误: %v", err)
	}
}
