package command

import (
	"go/types"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework"
)

// TestValidateApplicationImportPathRejectsCommandPayloads 验证外部包路径只接受
// Go 导入路径的安全字符集，不能把命令参数、路径穿越或平台分隔符带入 go list。
func TestValidateApplicationImportPathRejectsCommandPayloads(t *testing.T) {
	validPaths := []string{
		"github.com/example/project/v2",
		"go.opentelemetry.io/otel/trace",
		"gopkg.in/yaml.v3",
	}
	for _, importPath := range validPaths {
		if err := validateApplicationImportPath(importPath); err != nil {
			t.Fatalf("合法导入路径 %q 被拒绝: %v", importPath, err)
		}
	}
	invalidPaths := []string{
		"",
		"../example.com/project",
		"example.com//project",
		"example.com/project;whoami",
		"example.com\\project",
		" example.com/project",
		"example.com/project@latest",
	}
	for _, importPath := range invalidPaths {
		if err := validateApplicationImportPath(importPath); err == nil {
			t.Fatalf("非法导入路径 %q 未被拒绝", importPath)
		}
	}
}

// TestOpenApplicationExportFileAcceptsOnlyRegularFiles 验证导出数据读取入口
// 只接受绝对路径指向的普通文件，并通过受限目录句柄返回内容。
func TestOpenApplicationExportFileAcceptsOnlyRegularFiles(t *testing.T) {
	basePath := t.TempDir()
	exportPath := filepath.Join(basePath, "package.a")
	if err := os.WriteFile(exportPath, []byte("export-data"), 0o600); err != nil {
		t.Fatalf("写入导出数据测试文件失败: %v", err)
	}
	file, err := openApplicationExportFile(exportPath)
	if err != nil {
		t.Fatalf("打开合法导出数据失败: %v", err)
	}
	content, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("读取或关闭导出数据失败: read=%v close=%v", readErr, closeErr)
	}
	if string(content) != "export-data" {
		t.Fatalf("导出数据内容错误: %q", content)
	}
	if _, err := openApplicationExportFile("relative.a"); err == nil {
		t.Fatal("相对导出数据路径未被拒绝")
	}
	if _, err := openApplicationExportFile(basePath); err == nil {
		t.Fatal("目录被错误地当作导出数据文件")
	}
}

// TestDiscoverApplicationStructTypesUsesGoTypes 验证发现结果来自可实例化的
// Go 类型，而不是源码文本外形；开放泛型和标量别名不得进入复合字面量。
func TestDiscoverApplicationStructTypesUsesGoTypes(t *testing.T) {
	basePath := prepareSemanticDiscoveryModule(t)
	writeDiscoveryFixture(t, basePath, "app/index/controller/types.go", `package controller

import framework "github.com/zhuhanxin0308/thinkgo/framework"

var serviceName = framework.ServiceApp

type Generic[T any] struct{}

func (*Generic[T]) Index() string { return "generic" }

type Concrete = Generic[string]
type Scalar = string
type NamedScalar string

type Embedded struct {
	Concrete
}

func (*Embedded) Show() string { return "embedded" }
`)

	typeNames, hasSource, err := discoverApplicationStructTypes(basePath, "app/index/controller", "controller")
	if err != nil {
		t.Fatalf("语义发现控制器类型失败: %v", err)
	}
	if !hasSource {
		t.Fatal("存在控制器源码时必须报告包已存在")
	}
	if actual, expected := strings.Join(typeNames, ","), "Concrete,Embedded"; actual != expected {
		t.Fatalf("语义发现类型错误: got %q want %q", actual, expected)
	}
}

// TestDiscoverApplicationStructTypesPreservesRuntimeActionContract 验证发现期只
// 判定结构体可实例化性；空控制器、提升方法、歧义方法和运行期非法动作签名
// 仍会注册，具体动作由 HTTP 调度期按既有公开契约判定。
func TestDiscoverApplicationStructTypesPreservesRuntimeActionContract(t *testing.T) {
	basePath := prepareSemanticDiscoveryModule(t)
	writeDiscoveryFixture(t, basePath, "app/index/controller/actions.go", `package controller

type Empty struct{}

type InvalidAction struct{}

func (*InvalidAction) Broken() (string, string, string) {
	return "", "", ""
}

type actionBase struct{}

func (*actionBase) Promoted() string { return "promoted" }

type EmbeddedAction struct {
	actionBase
}

type leftAction struct{}
type rightAction struct{}

func (*leftAction) Shared() string  { return "left" }
func (*rightAction) Shared() string { return "right" }

type AmbiguousAction struct {
	leftAction
	rightAction
}
`)

	typeNames, _, err := discoverApplicationStructTypes(basePath, "app/index/controller", "controller")
	if err != nil {
		t.Fatalf("发现控制器方法集合失败: %v", err)
	}
	expected := "AmbiguousAction,EmbeddedAction,Empty,InvalidAction"
	if actual := strings.Join(typeNames, ","); actual != expected {
		t.Fatalf("控制器兼容发现结果错误: got %q want %q", actual, expected)
	}
}

// TestDiscoverApplicationStructTypesResolvesImportedAliases 验证项目内不同包名、
// 已实例化泛型和标准库类型都按真实底层类型判断，不按导入文本猜测。
func TestDiscoverApplicationStructTypesResolvesImportedAliases(t *testing.T) {
	basePath := prepareSemanticDiscoveryModule(t)
	writeDiscoveryFixture(t, basePath, "internal/foundation/types.go", `package base

type Record struct{}
type Box[T any] struct{}
`)
	writeDiscoveryFixture(t, basePath, "app/index/controller/imported.go", `package controller

import (
	"net/http"

	base "example.com/semantic/internal/foundation"
	framework "github.com/zhuhanxin0308/thinkgo/framework"
	frameworkcache "github.com/zhuhanxin0308/thinkgo/framework/cache"
	"github.com/zhuhanxin0308/thinkgo/framework/event"
	"github.com/zhuhanxin0308/thinkgo/framework/middleware"
)

type Local = base.Record
type ImportedGeneric = base.Box[string]
type HTTPRequest = http.Request
type HTTPHeader = http.Header
type FrameworkDefinition = framework.ApplicationDefinition
type FrameworkCacheOptions = frameworkcache.StoreOptions
type EventFactory = event.Factory
type MiddlewareAlias = middleware.Handler

type FrameworkServiceKeyBytes [len(framework.ServiceApp)]byte

type Embedded struct {
	Local
}
`)

	typeNames, _, err := discoverApplicationStructTypes(basePath, "app/index/controller", "controller")
	if err != nil {
		t.Fatalf("解析导入类型别名失败: %v", err)
	}
	if actual, expected := strings.Join(typeNames, ","), "Embedded,FrameworkCacheOptions,FrameworkDefinition,HTTPRequest,ImportedGeneric,Local"; actual != expected {
		t.Fatalf("导入类型别名发现错误: got %q want %q", actual, expected)
	}
}

// TestSemanticDiscoveryRequiresFrameworkFromTargetModule 验证发现器不会在目标
// go.mod 缺少框架依赖时回退到内置伪类型，并且失败前不发布任何生成文件。
func TestSemanticDiscoveryRequiresFrameworkFromTargetModule(t *testing.T) {
	basePath := t.TempDir()
	moduleSource := "module example.com/missing-framework\n\ngo 1.26.6\n"
	writeDiscoveryFixture(t, basePath, "go.mod", moduleSource)
	writeDiscoveryFixture(t, basePath, "app/index/controller/index.go", `package controller

import framework "github.com/zhuhanxin0308/thinkgo/framework"

type Index = framework.ApplicationDefinition
`)
	application := &framework.App{BasePath: basePath, ApplicationPath: filepath.Join(basePath, "app")}
	err := RefreshControllerDiscovery(application)
	if err == nil || !strings.Contains(err.Error(), frameworkImportPath) {
		t.Fatalf("缺少目标模块框架依赖必须返回可定位错误，实际为 %v", err)
	}
	currentModule, readErr := os.ReadFile(filepath.Join(basePath, "go.mod"))
	if readErr != nil || string(currentModule) != moduleSource {
		t.Fatalf("只读解析不得修改 go.mod: content=%q err=%v", currentModule, readErr)
	}
	for _, relativePath := range []string{"app/autoload_generated.go", "app/index/autoload_generated.go", "go.sum"} {
		if _, statErr := os.Stat(filepath.Join(basePath, filepath.FromSlash(relativePath))); !os.IsNotExist(statErr) {
			t.Fatalf("解析失败不得留下文件 %q: %v", relativePath, statErr)
		}
	}
}

// TestSemanticDiscoveryIgnoresAmbientWorkspace 验证外部 GOWORK 不能改变目标
// go.mod 选择的框架版本或替换规则。
func TestSemanticDiscoveryIgnoresAmbientWorkspace(t *testing.T) {
	basePath := prepareSemanticDiscoveryModule(t)
	invalidWorkspace := filepath.Join(t.TempDir(), "go.work")
	writeDiscoveryFixture(t, filepath.Dir(invalidWorkspace), filepath.Base(invalidWorkspace), "这不是合法的工作区文件\n")
	t.Setenv("GOWORK", invalidWorkspace)
	writeDiscoveryFixture(t, basePath, "app/index/controller/types.go", `package controller

import framework "github.com/zhuhanxin0308/thinkgo/framework"

type Definition = framework.ApplicationDefinition
`)

	typeNames, _, err := discoverApplicationStructTypes(basePath, "app/index/controller", "controller")
	if err != nil {
		t.Fatalf("环境工作区不得污染目标模块语义解析: %v", err)
	}
	if actual := strings.Join(typeNames, ","); actual != "Definition" {
		t.Fatalf("目标模块框架类型解析错误: %q", actual)
	}
}

// TestSemanticContextImportsFrameworkExportData 验证即使目标模块就是框架自身，
// 也能通过编译导出数据取得真实结构体和常量，不会形成发现器导入循环。
func TestSemanticContextImportsFrameworkExportData(t *testing.T) {
	frameworkPath, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("解析框架模块路径失败: %v", err)
	}
	context := newApplicationSemanticContext(frameworkPath, frameworkImportPath)
	frameworkPackage, err := context.Import(frameworkImportPath)
	if err != nil {
		t.Fatalf("从框架模块自身导入编译数据失败: %v", err)
	}
	definition, ok := frameworkPackage.Scope().Lookup("ApplicationDefinition").(*types.TypeName)
	if !ok {
		t.Fatal("真实框架导出数据缺少 ApplicationDefinition 类型")
	}
	if _, ok = types.Unalias(definition.Type()).Underlying().(*types.Struct); !ok {
		t.Fatalf("ApplicationDefinition 的真实底层类型不是结构体: %s", definition.Type())
	}
	if _, ok = frameworkPackage.Scope().Lookup("ServiceApp").(*types.Const); !ok {
		t.Fatal("真实框架导出数据缺少 ServiceApp 常量")
	}
}

// TestDiscoverApplicationConstructorsChecksReturnType 验证只有无参数且返回
// 对应控制器指针的函数才会被当作验证器构造器。
func TestDiscoverApplicationConstructorsChecksReturnType(t *testing.T) {
	basePath := prepareSemanticDiscoveryModule(t)
	writeDiscoveryFixture(t, basePath, "app/index/validate/types.go", `package validate

type Correct struct{}
type WrongResult struct{}
type ValueResult struct{}
type WithParameter struct{}
type Generic[T any] struct{}
type Concrete = Generic[string]

func NewCorrect() *Correct { return &Correct{} }
func NewWrongResult() string { return "wrong" }
func NewValueResult() ValueResult { return ValueResult{} }
func NewWithParameter(_ int) *WithParameter { return &WithParameter{} }
func NewConcrete() *Concrete { return &Concrete{} }
`)

	typeNames, _, err := discoverApplicationStructTypes(basePath, "app/index/validate", "validate")
	if err != nil {
		t.Fatalf("发现验证器类型失败: %v", err)
	}
	constructors, err := discoverApplicationConstructors(basePath, "app/index/validate", typeNames)
	if err != nil {
		t.Fatalf("发现验证器构造器失败: %v", err)
	}
	for _, expected := range []string{"Concrete", "Correct"} {
		if !constructors[expected] {
			t.Errorf("合法构造器 New%s 未被发现", expected)
		}
	}
	for _, unexpected := range []string{"WrongResult", "ValueResult", "WithParameter"} {
		if constructors[unexpected] {
			t.Errorf("非法构造器 New%s 不得被发现", unexpected)
		}
	}
}

// TestDiscoverApplicationEntryPointsAcceptsTypeAliases 验证应用入口允许 Go
// 语义上完全相同的类型别名，并能识别导入别名。
func TestDiscoverApplicationEntryPointsAcceptsTypeAliases(t *testing.T) {
	basePath := prepareSemanticDiscoveryModule(t)
	writeDiscoveryFixture(t, basePath, "app/index/entry.go", `package index

import (
	fw "github.com/zhuhanxin0308/thinkgo/framework"
	mw "github.com/zhuhanxin0308/thinkgo/framework/middleware"
)

type ServicesResult = []interface{}
type EventResult = fw.EventDefinition
type MiddlewareHandler = mw.Handler
type ProviderResult = map[string]interface{}

func Services() ServicesResult { return nil }
func Events() EventResult { return EventResult{} }
func Middleware() []MiddlewareHandler { return nil }
func Providers() ProviderResult { return nil }
`)

	entryPoints, err := discoverApplicationEntryPoints(basePath, "app/index", "index")
	if err != nil {
		t.Fatalf("类型别名入口应通过语义校验: %v", err)
	}
	if !entryPoints.services || !entryPoints.events || !entryPoints.middleware || !entryPoints.providers {
		t.Fatalf("类型别名入口发现不完整: %#v", entryPoints)
	}
}

// TestDiscoverApplicationEntryPointsRejectsWrongTypes 验证参数和结果数量相同
// 也不能绕过静态入口契约。
func TestDiscoverApplicationEntryPointsRejectsWrongTypes(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{name: "服务结果", source: "package index\n\nfunc Services() []string { return nil }\n"},
		{name: "事件结果", source: "package index\n\ntype EventDefinition struct{}\nfunc Events() EventDefinition { return EventDefinition{} }\n"},
		{name: "中间件结果", source: "package index\n\nfunc Middleware() []func() { return nil }\n"},
		{name: "容器绑定结果", source: "package index\n\nfunc Providers() map[string]string { return nil }\n"},
		{name: "泛型入口", source: "package index\n\nfunc Services[T any]() []interface{} { return nil }\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			basePath := prepareSemanticDiscoveryModule(t)
			writeDiscoveryFixture(t, basePath, "app/index/entry.go", test.source)
			if _, err := discoverApplicationEntryPoints(basePath, "app/index", "index"); err == nil || !strings.Contains(err.Error(), "应用入口 index.") {
				t.Fatalf("错误类型入口必须返回可定位错误，实际为 %v", err)
			}
		})
	}
}

// TestApplicationPackageExistsValidatesRouteLoader 验证 route 目录只有源码还
// 不足以生成调用，Load 必须接受框架 Route 指针且不返回结果。
func TestApplicationPackageExistsValidatesRouteLoader(t *testing.T) {
	tests := []struct {
		name   string
		source string
		valid  bool
	}{
		{
			name: "类型别名",
			source: `package route

import fw "github.com/zhuhanxin0308/thinkgo/framework"

type Route = fw.Route

func Load(route *Route) {}
`,
			valid: true,
		},
		{name: "缺少入口", source: "package route\n\ntype Definition struct{}\n"},
		{name: "错误参数", source: "package route\n\nfunc Load(route interface{}) {}\n"},
		{name: "错误结果", source: "package route\n\nfunc Load(route interface{}) error { return nil }\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			basePath := prepareSemanticDiscoveryModule(t)
			writeDiscoveryFixture(t, basePath, "app/index/route/app.go", test.source)
			exists, err := applicationPackageExists(basePath, "app/index/route", "route")
			if test.valid {
				if err != nil || !exists {
					t.Fatalf("合法路由入口未被发现: exists=%v err=%v", exists, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "路由入口 route.Load") {
				t.Fatalf("非法路由入口必须返回可定位错误，实际为 exists=%v err=%v", exists, err)
			}
		})
	}
}

func prepareSemanticDiscoveryModule(t *testing.T) string {
	t.Helper()
	basePath := t.TempDir()
	writeDiscoveryModuleFixture(t, basePath, "example.com/semantic")
	return basePath
}
