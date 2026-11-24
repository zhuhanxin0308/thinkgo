package command

import (
	"bytes"
	"go/format"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// 计入源码 CLI 每次重新选择 Go 源文件、读取导出数据和检查命令接口的成本。
// 预热构建缓存，保留每次真实的 Go 子进程；ReportAllocs 仅计本进程。
func BenchmarkDiscoverProjectCommandsWarmGoCache(b *testing.B) {
	base := prepareStaticProjectCommands(b)
	ctx := b.Context()
	want := []ProjectCommandType{
		{ImportPath: "example.com/commands/app/admin/command/report", TypeName: "Daily"},
		{ImportPath: "example.com/commands/app/index/command", TypeName: "Audit"},
	}
	check := func(got []ProjectCommandType, err error) {
		b.Helper()
		if err != nil || !slices.Equal(got, want) {
			b.Fatalf("命令的类型、包路径或注册顺序不符：%v，%v", got, err)
		}
	}
	check(DiscoverProjectCommandsContext(ctx, base))
	b.ReportAllocs()
	for b.Loop() {
		got, err := DiscoverProjectCommandsContext(ctx, base)
		b.StopTimer()
		check(got, err)
		b.StartTimer()
	}
}

// 测量 service:discover 对双应用源码的选择、类型检查、装配清单生成与格式化。
// 每轮使用新的语义上下文，Go 构建缓存预热；文件暂存、发布和校验均不计时。
func BenchmarkGenerateNativeApplicationSourcesWarmGoCache(b *testing.B) {
	app := prepareNativeApplicationDiscovery(b)
	warm, err := nativeApplicationDiscoverySources(app)
	if err != nil {
		b.Fatal(err)
	}
	assertGeneratedApplicationSources(b, warm)
	b.ReportAllocs()
	for b.Loop() {
		sources, err := nativeApplicationDiscoverySources(app)
		b.StopTimer()
		if err != nil {
			b.Fatal(err)
		}
		assertGeneratedApplicationSources(b, sources)
		b.StartTimer()
	}
}

func assertGeneratedApplicationSources(b *testing.B, sources []generatedApplicationSource) {
	b.Helper()
	packages := map[string]string{
		applicationDiscoveryFilename:                "main",
		"app/" + applicationDiscoveryFilename:       "app",
		"app/admin/" + applicationDiscoveryFilename: "admin",
		"app/index/" + applicationDiscoveryFilename: "index",
		"cmd/think/" + applicationDiscoveryFilename: "main",
		projectCommandDiscoveryPath:                 "commandautoload",
	}
	files := make(map[string]string, len(sources))
	for _, source := range sources {
		path := filepath.ToSlash(source.relativePath)
		expected, ok := packages[path]
		if !ok {
			b.Fatalf("生成了意外或重复的清单：%s", path)
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, source.source, parser.SkipObjectResolution)
		if err != nil || file.Name.Name != expected {
			b.Fatalf("清单 %s 不是有效的 %s 包源码：%v", path, expected, err)
		}
		delete(packages, path)
		files[path] = string(source.source)
	}
	if len(packages) != 0 {
		b.Fatalf("生成结果遗漏清单：%v", packages)
	}
	assertNativeApplicationRegistrations(b, func(path string) string { return files[path] })
	global := strings.Join(strings.Fields(files["app/"+applicationDiscoveryFilename]), "")
	for _, field := range []string{"Services", "Events", "Middleware", "Providers"} {
		if !strings.Contains(global, field+":"+field+"()") {
			b.Fatalf("公共装配清单未绑定 %s", field)
		}
	}
	if files["cmd/think/"+applicationDiscoveryFilename] != "//go:build thinkgo_runtime\n\n"+files[applicationDiscoveryFilename] {
		b.Fatal("运行时 CLI 与 HTTP 入口的应用注册清单不一致")
	}
	commands := strings.Join(strings.Fields(files[projectCommandDiscoveryPath]), "")
	if !strings.Contains(commands, "funcCommands()[]console.ICommand{return[]console.ICommand{}}") {
		b.Fatal("没有命令的双应用项目应生成空的命令注册函数")
	}
}

// 以原脚手架的 HTTP 调用、CLI 函数引用和两处 HTTP 测试调用测量 AST 迁移与格式化。
// 从现行模板恢复旧导入和旧绑定，迁移结果应逐字节还原现行模板。
func BenchmarkMigrateApplicationEntry(b *testing.B) {
	const modulePath = "example.com/legacy"
	const appImport = modulePath + "/app"
	files, err := projectSources(modulePath)
	if err != nil {
		b.Fatal(err)
	}
	for _, entry := range []struct {
		name, path, adjacentImport string
		references                 int
	}{
		{"HTTP", "main.go", frameworkImportPath, 1},
		{"CLI", "cmd/think/runtime.go", modulePath + "/cmd/think/commandautoload", 1},
		{"HTTPTests", "main_test.go", frameworkImportPath, 2},
	} {
		b.Run(entry.name, func(b *testing.B) {
			want := files[filepath.FromSlash(entry.path)]
			// 旧版业务包位于外部依赖组；从同组导入恢复原位置，以便逐字节核对迁移结果。
			anchor := []byte("\t\"" + entry.adjacentImport + "\"\n")
			if bytes.Count(want, []byte("registerApplications")) != entry.references || bytes.Count(want, anchor) != 1 {
				b.Fatalf("脚手架入口已改变，需要重核旧绑定：%s", entry.path)
			}
			insertion := "\tbusinessapp \"" + appImport + "\"\n" + string(anchor)
			legacy := bytes.Replace(want, anchor, []byte(insertion), 1)
			legacy = bytes.ReplaceAll(legacy, []byte("registerApplications"), []byte("businessapp.Register"))
			legacy, err := format.Source(legacy)
			if err != nil {
				b.Fatal(err)
			}
			b.SetBytes(int64(len(legacy)))
			b.ReportAllocs()
			var got []byte
			for b.Loop() {
				got, err = migrateApplicationEntry(legacy, appImport)
				if err != nil {
					b.Fatal(err)
				}
			}
			if !bytes.Equal(got, want) {
				b.Fatalf("迁移后入口与脚手架不符：%s\n%s", entry.path, got)
			}
		})
	}
}
