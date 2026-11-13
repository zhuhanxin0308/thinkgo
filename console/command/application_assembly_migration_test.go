package command

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	framework "github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/console"
)

// TestAssemblyMigrationRewritesOnlyResolvedImports 验证迁移依据导入与作用域而非字符串替换。
func TestAssemblyMigrationRewritesOnlyResolvedImports(t *testing.T) {
	for _, test := range []struct {
		name, source, want string
		invalid            bool
	}{
		{"默认导入", "package main\nimport \"example.com/project/app\"\nvar register = app.Register\n", "var register = registerApplications", false},
		{"别名及其他引用", "package main\nimport shared \"example.com/project/app\"\nvar register = shared.Register\nvar base shared.BaseController\n", "var base shared.BaseController", false},
		{"局部遮蔽", "package main\nimport shared \"example.com/project/app\"\nvar register = shared.Register\nfunc f(shared struct{Register int}) int { return shared.Register }\n", "return shared.Register", false},
		{"字符串及注释", "package main\nimport shared \"example.com/project/app\"\n// shared.Register 不应替换注释\nvar text = \"shared.Register\"\nvar register = shared.Register\n", `"shared.Register"`, false},
		{"点导入", "package main\nimport . \"example.com/project/app\"\nvar register = Register\n", "", true},
		{"外部测试包", "package main_test\nimport shared \"example.com/project/app\"\nvar register = shared.Register\n", "", true},
		{"非法源码", "package main\nfunc (", "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := migrateApplicationEntry([]byte(test.source), "example.com/project/app")
			if test.invalid {
				if err == nil {
					t.Fatal("无法安全识别的入口必须明确拒绝")
				}
				return
			}
			if err != nil || !strings.Contains(string(got), test.want) || !strings.Contains(string(got), "var register = registerApplications") {
				t.Fatalf("迁移结果错误: %s %v", got, err)
			}
		})
	}
}

// TestAssemblyMigrationRejectsHandwrittenConflicts 验证不覆盖同名源码，也不绕过手写注册逻辑。
func TestAssemblyMigrationRejectsHandwrittenConflicts(t *testing.T) {
	for _, test := range []struct{ path, source string }{
		{"autoload_generated.go", "package main\n// 手写入口\n"},
		{"cmd/think/autoload_generated.go", "package main\n// 手写命令入口\n"},
		{"main.go", "package main\nfunc registerApplications() {}\n"},
		{"app/custom.go", "package app\nfunc RegisterGlobals() {}\n"},
		{"app/custom.go", "package app\nfunc Register() {}\n"},
	} {
		t.Run(test.path+test.source, func(t *testing.T) {
			app, _ := prepareControllerDiscoveryTransactionFixture(t, false)
			if strings.Contains(test.source, "func Register()") {
				writeDiscoveryFixture(t, app.BasePath, "main.go", "package main\nimport app \"example.com/transaction/app\"\nvar register = app.Register\n")
			}
			writeDiscoveryFixture(t, app.BasePath, test.path, test.source)
			if err := refreshControllerDiscovery(app, true); err == nil || !strings.Contains(err.Error(), "手写") {
				t.Fatalf("手写文件冲突必须明确拒绝: %v", err)
			}
			if got := readGeneratedDiscoverySource(t, app.BasePath, test.path); got != test.source {
				t.Fatalf("手写文件被覆盖: %s", got)
			}
			if _, err := os.Stat(filepath.Join(app.BasePath, "app", "index", applicationDiscoveryFilename)); !os.IsNotExist(err) {
				t.Fatalf("预检失败不得发布部分清单: %v", err)
			}
		})
	}
}

// TestAssemblyMigrationCommandRejectsOverlay 验证只读构建模式不会隐式修改项目入口。
func TestAssemblyMigrationCommandRejectsOverlay(t *testing.T) {
	command := &ServiceDiscover{}
	command.SetApp(&framework.App{BasePath: t.TempDir()})
	command.Configure()
	input := console.NewInput("--migrate", "--overlay-dir", t.TempDir())
	if err := input.Parse(command.GetArgumentDefinitions(), command.GetOptionDefinitions()); err != nil {
		t.Fatal(err)
	}
	if err := command.Execute(input, generatorTestOutput()); err == nil || !strings.Contains(err.Error(), "不能同时") {
		t.Fatalf("互斥模式必须在写入前拒绝: %v", err)
	}
}

// TestAssemblyDiscoveryAcceptsWindowsCheckout 验证 Git 转换为 CRLF 后仍识别自有清单。
func TestAssemblyDiscoveryAcceptsWindowsCheckout(t *testing.T) {
	app, _ := prepareControllerDiscoveryTransactionFixture(t, true)
	for _, path := range []string{applicationDiscoveryFilename, "cmd/think/" + applicationDiscoveryFilename} {
		content := readGeneratedDiscoverySource(t, app.BasePath, path)
		writeDiscoveryFixture(t, app.BasePath, path, strings.ReplaceAll(content, "\n", "\r\n"))
	}
	if err := RefreshControllerDiscovery(app); err != nil {
		t.Fatalf("换行转换不应被误判为手写文件: %v", err)
	}
	if err := CheckControllerDiscovery(app); err != nil {
		t.Fatal(err)
	}
}

// TestAssemblyMigrationCompilesLegacyCycle 验证已形成循环的旧清单也能显式迁移。
func TestAssemblyMigrationCompilesLegacyCycle(t *testing.T) {
	base := t.TempDir()
	writeDiscoveryModuleFixture(t, base, "example.com/legacy")
	for path, source := range map[string]string{
		"main.go": `package main
import businessapp "example.com/legacy/app"
var register = businessapp.Register
`,
		"app/base_controller.go": `package app
import framework "github.com/zhuhanxin0308/thinkgo/v3"
type BaseController struct { framework.Controller }
`,
		"app/index/controller/index.go": `package controller
import "example.com/legacy/app"
type Index struct { app.BaseController }
`,
		"app/autoload_generated.go": `package app
import (
    framework "github.com/zhuhanxin0308/thinkgo/v3"
    "example.com/legacy/app/index"
)
func Register(app *framework.App) error {
    return app.RegisterApplications(func(*framework.App) error { return nil }, index.Definition())
}
`,
		"app/index/autoload_generated.go": `package index
import (
    framework "github.com/zhuhanxin0308/thinkgo/v3"
    "example.com/legacy/app/index/controller"
)
func Definition() framework.ApplicationDefinition {
    return framework.ApplicationDefinition{Name: "index", Register: func(app *framework.App) error {
        return app.RegisterController("Index", &controller.Index{})
    }}
}
`,
	} {
		writeDiscoveryFixture(t, base, path, source)
	}
	if err := refreshControllerDiscovery(&framework.App{BasePath: base}, true); err != nil {
		t.Fatal(err)
	}
	process := exec.Command("go", "test", "-mod=mod", "./...", "-run", "^$")
	process.Dir = base
	process.Env = append(os.Environ(), "GOWORK=off", "CGO_ENABLED=0")
	if output, err := process.CombinedOutput(); err != nil {
		t.Fatalf("旧项目迁移后仍无法编译: %v\n%s", err, output)
	}
}

// TestAssemblyMigrationIsExplicitAndAtomic 验证普通发现不改手写入口且迁移失败整批回滚。
func TestAssemblyMigrationIsExplicitAndAtomic(t *testing.T) {
	app, paths := prepareControllerDiscoveryTransactionFixture(t, true)
	entry := "package main\nimport businessapp \"example.com/project/app\"\nvar register = businessapp.Register\n"
	// 使用当前测试模块的真实路径构造旧入口。
	module, err := readApplicationModulePath(app.BasePath)
	if err != nil {
		t.Fatal(err)
	}
	entry = strings.ReplaceAll(entry, "example.com/project", module)
	writeDiscoveryFixture(t, app.BasePath, "main.go", entry)
	paths = append(paths, filepath.Join(app.BasePath, "main.go"), filepath.Join(app.BasePath, applicationDiscoveryFilename))
	snapshots := readControllerDiscoverySnapshots(t, paths)
	if err := RefreshControllerDiscovery(app); err == nil || !strings.Contains(err.Error(), "--migrate") {
		t.Fatalf("旧入口必须显式迁移: %v", err)
	}
	assertControllerDiscoverySnapshots(t, paths, snapshots)
	injected := errors.New("模拟迁移发布失败")
	original := linkControllerDiscoveryPublication
	t.Cleanup(func() { linkControllerDiscoveryPublication = original })
	linkControllerDiscoveryPublication = func(root *os.Root, oldPath, newPath string) error {
		if newPath == "main.go" {
			return injected
		}
		return original(root, oldPath, newPath)
	}
	if err := refreshControllerDiscovery(app, true); !errors.Is(err, injected) {
		t.Fatalf("迁移失败原因丢失: %v", err)
	}
	assertControllerDiscoverySnapshots(t, paths, snapshots)
	assertNoControllerDiscoveryAuxiliaryFiles(t, app.BasePath)
	linkControllerDiscoveryPublication = original
	if err := refreshControllerDiscovery(app, true); err != nil {
		t.Fatal(err)
	}
	got := readGeneratedDiscoverySource(t, app.BasePath, "main.go")
	if !strings.Contains(got, "registerApplications") || strings.Contains(got, "businessapp") {
		t.Fatalf("旧入口未迁移: %s", got)
	}
	if err := refreshControllerDiscovery(app, true); err != nil {
		t.Fatalf("重复迁移应幂等: %v", err)
	}
}

// TestAssemblyMigrationPreservesConcurrentEdits 验证计划生成后外部保存不会被迁移覆盖。
func TestAssemblyMigrationPreservesConcurrentEdits(t *testing.T) {
	base := t.TempDir()
	writeDiscoveryFixture(t, base, "main.go", "package main\nimport app \"example.com/project/app\"\nvar register = app.Register\n")
	sources, err := projectAssemblyMigrationSources(base, "example.com/project", true)
	if err != nil {
		t.Fatal(err)
	}
	changed := []byte("package main\n// 用户同时保存的新内容\n")
	writeDiscoveryFixture(t, base, "main.go", string(changed))
	if err := replaceGeneratedSourceBatch(base, sources); !errors.Is(err, ErrControllerDiscoveryPublishConflict) {
		t.Fatalf("并发编辑必须报冲突: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(base, "main.go"))
	if err != nil || !bytes.Equal(got, changed) {
		t.Fatalf("并发编辑被覆盖: %s %v", got, err)
	}
}
