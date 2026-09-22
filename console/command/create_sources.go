package command

import (
	"fmt"
	"go/format"
	"path/filepath"
)

const scaffoldGoVersion = "1.26.6"

func projectSources(module string) (map[string][]byte, error) {
	sources := nativeApplicationBuildSources("index")
	for name, source := range projectGlobalSources {
		sources[filepath.Join("app", name)] = source
	}
	sources["go.mod"] = "module " + module + "\n\ngo " + scaffoldGoVersion + "\n"
	sources["main.go"] = fmt.Sprintf(projectMainSource, module)
	sources["main_test.go"] = fmt.Sprintf(projectTestSource, module)
	sources[filepath.Join("cmd", "think", "main.go")] = projectConsoleSource
	sources[filepath.Join("cmd", "think", "runtime.go")] = fmt.Sprintf(projectRuntimeConsoleSource, module)
	sources[filepath.Join("app", "index", "view", "index.html")] = nativeApplicationWelcomeView("index")
	sources[filepath.Join("app", "lang", "zh-cn.json")] = "{\n    \"welcome\": \"欢迎使用 ThinkGo\"\n}\n"
	sources[filepath.Join("public", "robots.txt")] = "User-agent: *\nDisallow:\n"
	sources[".gitignore"] = "/dist/\n/runtime/\n.env\n.env.*\n"
	for name, content := range projectConfigSources {
		sources[filepath.Join("config", name+".json")] = content + "\n"
	}
	result := make(map[string][]byte, len(sources))
	for path, source := range sources {
		content := []byte(source)
		if filepath.Ext(path) == ".go" {
			var err error
			content, err = format.Source(content)
			if err != nil {
				return nil, fmt.Errorf("项目模板 %s 无法编译: %w", path, err)
			}
		}
		result[path] = content
	}
	return result, nil
}

const projectMainSource = `package main

import (
    "errors"
    "fmt"
    "os"
    "path/filepath"
    _ "time/tzdata" // 内置时区，发布包无需依赖系统时区文件。

    businessapp "%s/app"
    "github.com/zhuhanxin0308/thinkgo/v3"
    "github.com/zhuhanxin0308/thinkgo/v3/db/connector"
    fwhttp "github.com/zhuhanxin0308/thinkgo/v3/http"
)

func main() {
    if err := runHTTP(); err != nil {
        fmt.Fprintln(os.Stderr, err)
        os.Exit(1)
    }
}

// runHTTP 装配原生多应用内核，由 App 管理启动与关闭生命周期。
func runHTTP() error {
    connector.RegisterBuiltins()
    app := framework.NewApp(runtimeBasePath())
    if err := businessapp.Register(app); err != nil {
        return errors.Join(err, app.Close())
    }
    kernel, err := fwhttp.NewHttp(app)
    if err != nil {
        return errors.Join(err, app.Close())
    }
    app.Kernel = fwhttp.NewServer(kernel)
    return app.Run()
}

// runtimeBasePath 允许发布包从其他工作目录启动。
func runtimeBasePath() string {
    executable, err := os.Executable()
    if err == nil {
        if resolved, resolveErr := filepath.EvalSymlinks(executable); resolveErr == nil {
            executable = resolved
        }
        base := filepath.Dir(executable)
        config, configErr := os.Stat(filepath.Join(base, "config"))
        app, appErr := os.Stat(filepath.Join(base, "app"))
        if configErr == nil && appErr == nil && config.IsDir() && app.IsDir() {
            return base
        }
    }
    base, _ := os.Getwd()
    return base
}
`

const projectConsoleSource = `package main

import (
    "context"
    "fmt"
    "os"
    "os/signal"
    "syscall"
    "github.com/zhuhanxin0308/thinkgo/v3"
    "github.com/zhuhanxin0308/thinkgo/v3/console/launcher"
)

// registerBusiness 由运行时标签入口提供，源码命令不提前加载业务。
var registerBusiness func(*framework.App) error

func main() {
    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    err := launcher.Run(ctx, "", os.Args[1:], os.Stdout, os.Stderr, registerBusiness)
    stop()
    if err != nil {
        fmt.Fprintf(os.Stderr, "ThinkGo command failed: %q\n", err.Error())
        os.Exit(1)
    }
}
`

const projectRuntimeConsoleSource = `//go:build thinkgo_runtime

package main

import businessapp "%s/app"

// init 只在需要业务装配的命令宿主中注册应用。
func init() { registerBusiness = businessapp.Register }
`

const projectTestSource = `package main

import (
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
    businessapp "%s/app"
    "github.com/zhuhanxin0308/thinkgo/v3"
    fwhttp "github.com/zhuhanxin0308/thinkgo/v3/http"
)

// TestWelcome 验证真实路由与默认控制器，不启动网络监听服务。
func TestWelcome(t *testing.T) {
    app := framework.NewConsoleAppUninitialized(".")
    t.Cleanup(func() { _ = app.Close() })
    if err := businessapp.Register(app); err != nil { t.Fatal(err) }
    if err := app.Initialize(); err != nil { t.Fatal(err) }
    host, err := fwhttp.NewHttp(app)
    if err != nil { t.Fatal(err) }
    response := httptest.NewRecorder()
    host.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://localhost/", nil))
    if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "ThinkGo") {
        t.Fatalf("首页响应异常: %%d %%s", response.Code, response.Body.String())
    }
}
`

var projectConfigSources = map[string]string{
	"app":        `{"default_app":"index","default_timezone":"Asia/Shanghai","with_route":true,"public_path":"public","server":{"host":"0.0.0.0","port":8000},"show_error_msg":false}`,
	"database":   `{"default":"mysql","auto_timestamp":true,"connections":{"mysql":{"type":"mysql","hostname":"127.0.0.1","hostport":"3306","database":"","username":"root","password":"","charset":"utf8mb4","fields_cache":false}}}`,
	"cache":      `{"default":"file","stores":{"file":{"type":"File","path":"","prefix":"","expire":0,"tag_prefix":"tag:"}}}`,
	"session":    `{"name":"THINKGOSESSID","type":"file","expire":1440,"prefix":""}`,
	"log":        `{"default":"file","channels":{"file":{"type":"File","path":"","max_files":30,"json":false,"realtime_write":false}}}`,
	"view":       `{"type":"Think","view_dir_name":"view","view_suffix":"html","tpl_begin":"{","tpl_end":"}"}`,
	"lang":       `{"default_lang":"zh-cn","auto_detect_browser":true,"allow_lang_list":[],"detect_var":"lang","use_cookie":true,"cookie_var":"think_lang","header_var":"think-lang"}`,
	"route":      `{"url_route_must":false,"default_route_pattern":"[\\w\\.]+","default_controller":"Index","default_action":"index","url_html_suffix":"html","controller_layer":"controller"}`,
	"filesystem": `{"default":"local","disks":{"local":{"type":"local","root":"./runtime/storage"},"public":{"type":"local","root":"./public/storage","url":"/storage","visibility":"public"}}}`,
	"middleware": `{"alias":{},"priority":[]}`,
	"console":    `{"commands":[]}`,
	"cookie":     `{"expire":0,"path":"/","domain":"","secure":false,"httponly":true,"samesite":"lax"}`,
	"trace":      `{"type":"Html"}`,
}
