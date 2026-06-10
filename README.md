# ThinkGo

ThinkGo 是一个参考 ThinkPHP 使用习惯实现的 Go Web 框架。框架保留控制器、模型、视图、路由、中间件、配置、事件、验证、缓存、会话、日志和命令行等核心概念，同时按 Go 的长期运行进程、显式依赖和并发安全方式实现。

## 环境要求

- Go 版本以 `go.mod` 为准，当前为 `go 1.25.3`。
- 运行 Web 服务前必须保证 `config/database.json` 或 `.env` 中的数据库配置可用。
- 如启用 TLS 或 HTTP/3，证书文件必须存在，默认读取 `runtime/cert.pem` 和 `runtime/key.pem`。

## 目录规范

```text
thinkgo/
├── app/
│   ├── controller/      # 控制器，必须通过 init 注册
│   ├── middleware/      # 应用全局中间件，必须通过 init 注册
│   ├── model/           # 业务模型
│   ├── validate/        # 验证器
│   └── lang/            # 多语言 JSON 文件
├── cmd/
│   └── think/           # 命令行入口
├── config/              # JSON 配置文件
├── framework/           # 框架核心
├── public/              # 静态资源目录
├── route/               # 路由定义
├── runtime/             # 日志、缓存、会话、证书等运行时文件
└── main.go              # HTTP 服务入口
```

业务代码放在 `app`、`route`、`config` 和 `cmd`。框架核心能力放在 `framework`，除修复框架能力外不要把业务逻辑写入框架目录。

## 启动方式

```bash
go run main.go
```

默认 HTTP 配置在 `config/app.json`。命令行工具使用：

```bash
go run cmd/think/main.go list
go run cmd/think/main.go route:list
go run cmd/think/main.go config:dump
go run cmd/think/main.go run
```

每次修改代码后必须运行：

```bash
go test ./...
```

## 入口规范

HTTP 入口必须导入控制器、中间件和路由包，触发各包的 `init` 自动注册。

```go
package main

import (
	_ "thinkgo/app/controller" // 注册控制器
	_ "thinkgo/app/middleware" // 注册全局中间件
	"thinkgo/framework"
	"thinkgo/framework/http"
	_ "thinkgo/route" // 注册路由
)

func main() {
	app := framework.NewApp()
	app.Kernel = http.NewHttp(app)
	app.Run()
}
```

不要在入口里直接写业务路由、业务中间件或业务控制器逻辑。

## 配置规范

配置文件统一放在 `config/*.json`，文件名即一级配置命名空间，例如 `config/app.json` 对应 `app`，`config/database.json` 对应 `database`。

配置读取使用点路径：

```go
debug := app.Config.Get("app.app_debug", false).(bool)
port := app.Config.Get("app.server.port", 8080)
```

配置优先级：

1. `.env` 或系统环境变量。
2. `config/*.json`。
3. 框架默认值。

常用环境变量：

```text
APP_DEBUG=true
APP_TRACE=true
SERVER_HOST=0.0.0.0
SERVER_PORT=8081
SERVER_TLS_ENABLE=false
SERVER_HTTP3=false
DB_TYPE=mysql
DB_HOST=127.0.0.1
DB_PORT=3306
DB_USER=root
DB_PASS=
DB_NAME=thinkgo
```

## 路由用法

路由必须集中定义在 `route` 包，并通过 `framework.RegisterRouteLoader` 注册加载函数。

```go
package route

import (
	"thinkgo/framework"
	"thinkgo/framework/context"
)

func init() {
	framework.RegisterRouteLoader(Load)
}

func Load(app *framework.App) {
	app.Route.Get("/", func(req *context.Request) *context.Response {
		return context.NewResponse().Content("ThinkGo")
	}).Name("home")

	app.Route.Get("/api/users", "User@Index").Name("users.index")
	app.Route.Post("/api/users", "User@Save")
	app.Route.Get("/api/users/:id", "User@Read").Pattern("id", `\d+`)
}
```

支持的路由能力：

- `Get`、`Post`、`Put`、`Delete`、`Patch`、`Options`、`Head`、`Any`。
- `Group(prefix, fn, middlewares...)`。
- `Domain(domain, fn, middlewares...)`。
- `DomainGroup(domain, prefix, fn, middlewares...)`。
- `Resource(path, controller).Only(...)` 和 `Except(...)`。
- `Name(name)` 命名路由。
- `Pattern(param, regex)` 参数正则。
- `Ext(ext)` 后缀约束。
- `Miss(handler)` 兜底路由。
- `Redirect(path, target, code...)` 重定向路由。

控制器路由使用 `"Controller@Action"`。闭包路由必须返回 `*context.Response`。

## 控制器用法

控制器放在 `app/controller`，包名必须是 `controller`。每个控制器必须在 `init` 中注册，注册名必须和路由中的控制器名一致。

```go
package controller

import "thinkgo/framework"

// User 用户控制器。
type User struct {
	framework.Controller
}

func init() {
	// 每次请求都会创建新的控制器实例，避免并发请求共享状态。
	framework.RegisterController("User", &User{})
}

// Index 返回用户列表。
func (c *User) Index() string {
	return "User List"
}
```

控制器可返回：

- `string`：按文本响应。
- `*context.Response`：完全控制状态码、头部和响应体。
- 其他结构体或 map：自动 JSON 响应。

控制器常用方法：

```go
c.Success(data, "操作成功")
c.Error("参数错误", 400)
c.Result(data, 0, "success")
c.Redirect("/login")
c.Assign("name", "ThinkGo")
c.Fetch("index/index")
c.Lang("message.key")
```

不要把请求级状态写入包级变量或单例服务；请求数据应放在 `c.Request`、`req.Set()` 或显式传参中。

## 请求与响应

请求对象为 `*context.Request`。

```go
id := req.ParamInt("id", 0)
name := req.Get("name", "")
token := req.Header("Authorization")
body, err := req.Body()
file, err := req.File("avatar")
all := req.All()
only := req.Only("name", "email")
```

参数优先级：

```text
路由参数 > 表单/查询参数 > JSON 参数
```

响应对象为 `*context.Response`。

```go
return context.NewResponse().Code(201).Json(map[string]interface{}{
	"code": 0,
	"msg":  "created",
})
```

常用响应能力：

- `Content(text)` 文本。
- `Json(data)` JSON。
- `Jsonp(callback, data)` JSONP，回调名会做安全校验。
- `Xml(data)` XML。
- `Redirect(url, code...)` 重定向。
- `Download(path, filename)` 文件下载。
- `Stream(writer)` 流式输出。
- `Chunk(chunks)` 分块输出。
- `NoContent()` 204 响应。
- `Abort(code, payload)` 快速终止响应。

## 中间件用法

中间件函数签名固定为：

```go
func(req *context.Request, next func(*context.Request) *context.Response) *context.Response
```

应用全局中间件放在 `app/middleware`，包名必须是 `middleware`，并在 `init` 中注册。

```go
package middleware

import (
	"thinkgo/framework"
	"thinkgo/framework/context"
)

func init() {
	framework.RegisterGlobalMiddleware(Auth)
}

// Auth 校验请求身份。
func Auth(req *context.Request, next func(*context.Request) *context.Response) *context.Response {
	if req.Header("Authorization") == "" {
		return context.NewResponse().Abort(401, map[string]interface{}{"message": "unauthorized"})
	}
	return next(req)
}
```

路由级中间件直接作为路由参数传入：

```go
app.Route.Get("/api/profile", "User@Profile", middleware.Auth)
```

需要响应发送后的终结逻辑时，使用 `middleware.Lifecycle` 或 `PipeLifecycle`。

## 数据库用法

数据库配置在 `config/database.json`。默认连接通过 `app.DB` 使用，多连接通过 `app.DBManager.Connection(name)` 获取。

```go
user, err := app.DB.Name("users").
	WhereField("id", "=", 1).
	Find()

rows, err := app.DB.Name("users").
	WhereLike("name", "%张%").
	Order("id DESC").
	Page(1, 20).
	Select()

id, err := app.DB.Name("users").Insert(map[string]interface{}{
	"name": "张三",
	"age":  18,
})
```

安全规范：

- 默认使用 `WhereField`、`WhereMap`、`WhereIn`、`WhereBetween` 等参数化 API。
- 只有复杂 SQL 表达式才使用 `WhereRaw`，并且必须手动保证参数化。
- `Update` 和 `Delete` 禁止无 `WHERE` 条件，避免误更新或误删除全表。
- 表名和字段名必须使用安全标识符，不要拼接用户输入。

事务用法：

```go
err := app.DB.Transaction(func(tx *db.Tx) error {
	if _, err := tx.Table("users").Insert(data); err != nil {
		return err
	}
	return nil
})
```

## 模型用法

模型放在 `app/model`。表名必须明确，除非确认默认蛇形命名符合真实表名。

```go
package model

import "thinkgo/framework/db"

// User 用户模型。
type User struct {
	*db.Model
}

func NewUser(database *db.DB) *User {
	m := &User{}
	m.Model = db.NewModel(database, "users")
	return m
}
```

模型支持：

- `Find`、`Select`、`Insert`、`UpdateMap`、`DeleteRecord`。
- `AutoTimestamp`、`CreateTimeField`、`UpdateTimeField`。
- `SoftDelete`、`WithTrashed`、`OnlyTrashed`、`Restore`、`ForceDelete`。
- `Getter`、`Setter`、`Searcher`。
- `On` 注册模型事件。
- `DefineHasOne`、`DefineHasMany`、`DefineBelongsTo`、`DefineBelongsToMany`。

表命名规范：

- `db.NewModelAuto()` 只把结构体名转为 `snake_case`。
- 不会自动复数化。
- 表前缀由数据库配置统一追加。
- 实际表名不匹配时必须使用 `db.NewModel(database, "actual_table")`。

## 验证器用法

验证器放在 `app/validate`，规则使用 ThinkPHP 风格的字符串组合。

```go
v := validate.NewValidator()
v.Rule = map[string]string{
	"email": "required|email",
	"age":   "integer|min:1",
}
v.Message = map[string]string{
	"email.required": "邮箱不能为空",
}

if !v.Check(req.All()) {
	return c.Error(v.GetError(), 422)
}
```

支持场景：

```go
v.Scene = map[string][]string{
	"create": {"email", "age"},
}

ok := v.SetScene("create").Check(data)
```

常用规则包括 `required`、`number`、`integer`、`float`、`boolean`、`email`、`array`、`accepted`、`date`、`alpha`、`alphaNum`、`alphaDash`、`chs`、`ip`、`url`、`in`、`between`、`length`、`max`、`min`、`eq`、`gt`、`lt`、`regex`、`confirm`、`different`、`mobile`、`dateFormat`、`after`、`before`、`requireIf`、`requireWith`、`idCard`。

## 视图用法

视图默认使用 Go 模板驱动。模板目录由 `config/view.json` 配置。

```go
func (c *User) Page() string {
	c.Assign("title", "用户列表")
	return c.Fetch("user/index")
}
```

模板中可使用框架注册的 `lang` 函数读取多语言内容。

## 缓存用法

缓存配置在 `config/cache.json`。

```go
app.Cache.Set("user:1", user, time.Minute)
user := app.Cache.Get("user:1")
app.Cache.Forget("user:1")

app.Cache.Tag("user").Set("user:1", user, time.Minute)
app.Cache.Tag("user").Flush()

lock := app.Cache.Lock("sync:user", 10*time.Second)
if lock.Acquire() {
	defer lock.Release()
}
```

可用能力包括 `Get`、`Set`、`Has`、`Forget`、`Forever`、`Flush`、`Remember`、`Inc`、`Dec`、`Store`、`Tag` 和 `Lock`。

## Cookie 与 Session

Cookie 配置在 `config/cookie.json`，Session 配置在 `config/session.json`。

启用 Session：

```json
{
  "session_enable": true
}
```

控制器中优先通过请求上下文和框架 Session 管理器读写会话，不要自行解析会话文件。

## 事件用法

事件调度器通过 `app.Event` 使用。

```go
type UserCreatedListener struct{}

func (l *UserCreatedListener) Handle(e event.Event) {
	// 处理用户创建事件。
}

app.Event.Listen("user.created", &UserCreatedListener{})
app.Event.Dispatch(event.NewEvent("user.created", userID))
```

框架内置生命周期事件：

- `framework.AppInit`
- `framework.HttpRun`
- `framework.HttpEnd`
- `framework.RouteLoaded`
- `framework.LogWrite`
- `framework.LogRecord`

监听器可以设置优先级，数值越大越先执行。

## 日志用法

日志配置在 `config/log.json`。默认日志器为 `app.Log`。

```go
app.Log.Info("服务启动")
app.Log.ErrorCtx("创建用户失败", map[string]interface{}{
	"user_id": userID,
	"error":   err.Error(),
})

app.Log.Channel("sql").Info("查询完成")
```

日志器支持多驱动、多通道、级别过滤、调用位置记录、异步批量写入和关闭时刷盘。长期运行服务退出前必须调用 `Shutdown()`，`app.Run()` 已处理这一点。

## 命令行用法

命令入口在 `cmd/think/main.go`。当前注册的命令：

```text
list
version
clear
run
route:list
config:dump
make:controller
make:model
make:command
make:validate
make:middleware
make:event
make:listener
make:subscribe
make:service
```

新增命令应放在 `framework/console/command` 或应用自定义命令目录，并实现：

```go
type DemoCommand struct {
	console.Command
}

func (c *DemoCommand) Configure() {
	c.Signature = "app:demo"
	c.Description = "执行应用命令"
}

func (c *DemoCommand) Execute(input *console.Input, output *console.Output) {
	output.Writeln("done")
}
```



当前应用层集成测试验证：

- `app/controller` 控制器注册。
- `app/middleware` 全局中间件注册。
- `route` 路由加载器注册。
- HTTP 内核可以通过 `/api/users` 调度到 `User@Index`。



## 当前应用入口

当前项目内置了：

- `GET /`：返回 `ThinkGo`。
- `GET /api/users`：调度到 `User@Index`，返回 `User List`。
- 全局 `RequestID` 中间件：读取或生成 `X-Request-ID`，写入请求上下文并透传到响应头。
