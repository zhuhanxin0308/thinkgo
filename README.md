# ThinkGo

ThinkGo 是一个参考 ThinkPHP 使用习惯实现的 Go Web 框架。框架保留控制器、模型、视图、路由、中间件、配置、事件、验证、缓存、会话、日志和命令行等核心概念，同时按 Go 的长期运行进程、显式依赖和并发安全方式实现。

## 环境要求

- Go 版本以 `go.mod` 为准，当前为 `go 1.25.7`（由 `github.com/microsoft/go-mssqldb` 的最低版本要求决定）。本机 Go 低于该版本时，`GOTOOLCHAIN=auto`（默认）会自动拉取匹配工具链。
- 运行 Web 服务前必须保证 `config/database.json` 或 `.env` 中的数据库配置可用。
- 如启用 TLS 或 HTTP/3，证书文件必须存在，默认读取 `runtime/cert.pem` 和 `runtime/key.pem`。
- 依赖统一通过 Go Modules 管理，详见[依赖管理](#依赖管理)。完整开发约束见 [`docs/开发规范.md`](docs/开发规范.md)。

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
正式环境
```bash
go run main.go
```

开发环境--支持热重载
```bash
#linux
go run cmd/think/main.go run -p 8080
# window
go run .\cmd\think\main.go run -p 8080 
```

默认 HTTP 配置在 `config/app.json`。命令行工具使用：

```bash
go run cmd/think/main.go list
go run cmd/think/main.go route:list
go run cmd/think/main.go config:dump
go run cmd/think/main.go run            # 使用配置/默认端口启动
go run cmd/think/main.go run -p 9000    # 指定监听端口（-p 或 --port），覆盖配置与 .env
```

`run` 命令会自动探测 [air](https://github.com/air-verse/air)：已安装时以热重载方式启动（修改代码自动重新构建），未安装时以普通模式启动并提示安装方法。两种模式下 `-p` 均生效——指定的端口通过环境变量传递给热重载子进程，且优先级高于 `.env` 中的 `SERVER_PORT`。

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

配置读取使用点路径。**优先使用类型安全访问器**，避免直接对 `Get` 的返回值做类型断言（配置项缺失或类型不符时断言会 panic）：

```go
// 推荐：类型安全，缺失或类型不符时回退默认值，不会 panic
debug := app.Config.GetBool("app.app_debug", false)
port := app.Config.GetInt("app.server.port", 8080)
name := app.Config.GetString("app.app_name", "ThinkGo")
server := app.Config.GetMap("app.server") // 始终返回非 nil map

// 仅在确定类型时才使用裸 Get
raw := app.Config.Get("app.server.tls")
```

配置优先级（从高到低）：

1. 系统环境变量（真实进程环境变量，最高优先级）。
2. `.env` 文件。
3. `config/*.json`。
4. 框架默认值。

环境变量遵循 12-factor 约定：**真实系统环境变量优先于 `.env` 文件**，便于容器/CI/命令行（如 `run -p` 注入的 `SERVER_PORT`）覆盖仓库里的 `.env` 默认值；`.env` 仅作为本地兜底。框架加载 `.env` 时只写入内部存储，不会调用 `os.Setenv` 污染进程环境（避免 `DB_PASS` 等敏感值被子进程继承）。

> 注意：环境变量覆盖仅对框架显式桥接的键生效（`APP_DEBUG`、`APP_TRACE`、`SERVER_*`、`DB_*` 等）。其余配置项只走 `config/*.json` 与默认值。

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

### 路由配置与自动路由

`config/route.json` 控制路由解析行为，并在应用初始化时由框架接入（`applyRouteConfig`）：

```json
{
  "url_route_must": true,
  "default_controller": "Index",
  "default_action": "index"
}
```

- `url_route_must`：`true`（默认，**安全基线**）表示只允许显式注册的路由；`false` 时开启自动路由——未命中显式路由的 URL 会按 `/控制器/动作` 自动解析为 `Controller@Action`。
- **安全提示**：自动路由会把控制器上所有导出方法暴露为可达端点（框架已屏蔽基类内置方法）。仅在受控场景开启，且不要在控制器里放不希望被路由触达的导出辅助方法。
- `default_controller` / `default_action`：自动路由下根路径与缺省动作的回退目标。

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
- `Download(path, filename)` 文件下载（`path` 必须可信；`filename` 会被净化以防头注入）。
- `DownloadSafe(baseDir, relativePath, filename)` 安全下载：`relativePath` 可来自用户输入，框架将其限制在 `baseDir` 内，越界返回 403。
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

控制器级中间件通过别名声明，由分发器按动作过滤后应用。先在管道注册别名，再在控制器 `Init` 中声明：

```go
// 注册别名（通常在应用启动阶段）
app.Middleware.Alias("auth", middleware.Auth)

// 控制器中声明：Only 仅命中列表内动作，Except 排除列表内动作，二者都空则全部动作生效
func (c *User) Init(app *framework.App, req *context.Request) {
	c.Controller.Init(app, req)
	c.SetMiddleware(framework.ControllerMiddleware{Name: "auth", Except: []string{"Login"}})
}
```

执行顺序为：全局中间件 → 路由中间件 → 控制器中间件 → 控制器动作。

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

关联查询（JOIN）会对 JOIN 表套用与主表一致的前缀规则：`Name()` 创建的查询给 JOIN 表追加前缀，`Table()` 创建的查询按完整表名处理。JOIN 条件仅支持安全的 `字段 op 字段` 形式（支持 `table.field` 与 `AND` 连接），紧凑写法与带空格写法均可。

事务用法：

```go
err := app.DB.Transaction(func(tx *db.Tx) error {
	if _, err := tx.Table("users").Insert(data); err != nil {
		return err
	}
	return nil
})
```

带上下文与隔离级别的事务（推荐在 HTTP 请求中使用，便于随请求取消/超时回滚）：

```go
import (
	"context"
	"database/sql"
)

// 把请求上下文传入，请求取消/超时时事务自动回滚。
err := app.DB.TransactionContext(req.Raw().Context(), func(tx *db.Tx) error {
	// 事务内经 tx.Table / tx.Name 创建的查询自动继承该 context。
	if _, err := tx.Name("users").WhereField("id", "=", 1).Update(data); err != nil {
		return err
	}
	return nil
})

// 需要指定隔离级别时使用 BeginTx：
tx, err := app.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
```

### 查询上下文（超时与取消）

链式查询可通过 `WithContext` 绑定请求上下文，底层 SQL 执行将随上下文超时/取消，避免慢查询在请求结束后仍占用连接池：

```go
ctx, cancel := context.WithTimeout(req.Raw().Context(), 3*time.Second)
defer cancel()

rows, err := app.DB.Name("orders").
	WithContext(ctx).
	WhereField("status", "=", 1).
	Select()
```

模型查询同样支持 `mq.WithContext(ctx)`。未设置时回退到 `context.Background()`。

> 说明：context 仅对实现了 `ContextualConnection` 的连接（如内置的 SQL 连接）生效；Mongo/Neo4j 等连接自带超时控制。

### 统计与分页（JOIN / GROUP / DISTINCT）

`Count()` 和 `Paginate()` 会正确处理 `JOIN`、`GROUP BY`、`HAVING`、`DISTINCT`——此时框架自动对完整查询包裹一层 `SELECT COUNT(*) FROM (...)`：

```go
// JOIN：统计连接过滤后的真实行数；分页 total 准确。
p, err := app.DB.Name("orders").
	Join("users", "orders.user_id = users.id").
	Where("status = ?", 1).
	Paginate(1, 20)

// DISTINCT：统计去重后的行数。
n, err := app.DB.Name("orders").Distinct().Field("user_id").Count()
```

### 大数据量遍历（游标分页）

- `Chunk(count, cb)`：基于 `OFFSET` 的分块遍历。实现简单，但大表深分页性能随页码下降，且遍历期间增删数据可能漏读/重复。
- `ChunkById(count, pk, cb)`：基于主键游标（`WHERE id > ? ORDER BY id LIMIT count`）的分块遍历，**推荐用于大表全量遍历**，无 OFFSET 性能问题。

```go
err := app.DB.Name("users").ChunkById(1000, "id", func(rows []map[string]interface{}) bool {
	// 处理本批数据；返回 false 可提前终止。
	return true
})
```

### 批量插入

`InsertAll` 在占位符总数（行数 × 列数）超过驱动上限时自动分批；多批写入会包裹在事务中以保证原子性：

```go
affected, err := app.DB.Name("users").InsertAll([]map[string]interface{}{
	{"name": "a", "age": 1},
	{"name": "b", "age": 2},
})
```

要求所有行的字段集合一致，否则报错（避免错位写入脏数据）。

### 悲观锁

`Lock(true)` 排他锁、`Lock(false)` 共享锁。框架按方言翻译锁子句：

| 方言 | 排他锁 | 共享锁 |
|---|---|---|
| MySQL | `FOR UPDATE` | `LOCK IN SHARE MODE` |
| PostgreSQL | `FOR UPDATE` | `FOR SHARE` |
| SQLite | 不输出（无行级锁） | 不输出 |

### 多方言与 PostgreSQL 注意事项

- 框架统一以 `?` 占位符构建 SQL，执行前按方言转换（MySQL/SQLite `?`、PostgreSQL `$N`、SQL Server `@pN`、Oracle `:N`）。
- 分页语法自动适配：MySQL/PostgreSQL/SQLite 用 `LIMIT/OFFSET`，SQL Server/Oracle 用 `OFFSET … FETCH`（无显式排序时 SQL Server 会补默认排序以满足语法）。
- **PostgreSQL 自增主键回传**：`lib/pq` 不支持 `LastInsertId()`，框架自动改用 `INSERT … RETURNING id`，因此自增主键表的主键列需命名为 `id`（或在模型层显式回传）。

### 错误日志脱敏

数据库错误日志只记录字段名、SQL 模板与参数数量，**不记录字段值与绑定参数值**，避免密码、令牌、个人信息等通过日志泄露。

### 表达式条件 `WhereExp` 的安全边界

`WhereExp(field, op, expr)` 只接受“列名 + 白名单函数（如 `NOW()`、`COUNT()`）+ 算术运算 + `?` 占位符”构成的安全表达式，禁止引号字符串、分号、注释符及 `SELECT/UNION/OR/AND` 等关键字（去空格写法也会被识别拒绝）。需要更复杂的原始表达式时请改用 `WhereRaw` 并自行保证参数化。

### 性能调优（可选）

如需减少 MySQL 每条查询的预编译往返，可在 `config/database.json` 的连接参数中设置 `interpolateParams=true`（go-sql-driver 会在客户端安全转义参数）。框架默认保留服务端预编译以兼顾安全与稳健。

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
- `DefineHasOne`、`DefineHasMany`、`DefineBelongsTo`、`DefineBelongsToMany`（预加载按关联模型的主键关联，非默认主键请用 `PrimaryKey` 显式声明）。
- `WithContext` 绑定请求上下文；`ChunkById` 主键游标遍历；`Paginate` 对 JOIN/GROUP/DISTINCT 统计正确的 total。
- 终端方法（`Find`/`Count`/`Select` 等）在克隆上执行，同一查询对象可安全重复使用。

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

驱动行为统一：`file` 与 `redis` 驱动都会把任意值 JSON 序列化后存储，因此 `map`、`struct`、`slice` 都可缓存（JSON 反序列化后数值统一为 `float64`）。

Redis store 推荐配置 `prefix`，使 `Flush()` 只按前缀清理本应用键，避免误清同库其它业务数据；未配置 `prefix` 时 `Flush()` 退化为 `FLUSHDB`（仅影响所选 DB 索引）：

```json
{
  "stores": {
    "redis": {
      "type": "redis",
      "host": "127.0.0.1",
      "port": 6379,
      "password": "",
      "select": 0,
      "prefix": "thinkgo:",
      "timeout_ms": 3000
    }
  }
}
```

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

`run` 命令支持 `-p`/`--port` 指定监听端口（覆盖配置与 `.env`，详见[启动方式](#启动方式)）。除 `run` 外，其余命令（`version`、`list`、`make:*` 等）不需要数据库，命令行入口对它们使用 `framework.NewConsoleApp()` 跳过连库，避免每次执行都尝试连接数据库并打印连接错误。

新增命令应放在 `framework/console/command` 或应用自定义命令目录，并实现：

```go
type DemoCommand struct {
	console.Command
}

func (c *DemoCommand) Configure() {
	c.Signature = "app:demo"
	c.Description = "执行应用命令"
	// 声明参数与选项：会被 list 帮助展示，并由框架在执行前自动解析。
	c.AddArgument("name", "目标名称", true)         // 位置参数
	c.AddOption("output", "o", "输出目录", "./dist") // 带值选项：--output/-o，含默认值
	c.AddBoolOption("force", "f", "强制覆盖")        // 布尔开关：--force/-f
}

func (c *DemoCommand) Execute(input *console.Input, output *console.Output) {
	name := input.GetArgument(0)      // 位置参数（已剔除选项）
	dir := input.GetOption("output")  // 取值选项，未传时返回默认值 ./dist
	force := input.GetOption("force") // 布尔开关出现时为 "true"
	output.Writeln(name + " -> " + dir + " force=" + force)
}
```

选项写法支持 `--name value`、`--name=value`、短选项 `-n value`、`-n=value` 以及布尔开关 `--flag`；短选项会按声明解析为规范长名。

## 依赖管理

- 依赖统一使用 Go Modules，不提交 `vendor/`。新增依赖后必须运行 `go mod tidy` 保持 `go.mod`/`go.sum` 干净。
- 升级依赖：

  ```bash
  go get -u ./...   # 同主版本内升到最新 minor/patch
  go mod tidy
  go build ./... && go test ./...
  ```

- 跨大版本升级（如 `module/v2`）属破坏性变更，必须单独评估并配套改造代码与测试，不在常规升级内顺带进行。
- 升级后若有依赖抬高了 `go` 指令最低版本（例如 `microsoft/go-mssqldb` 要求 `go 1.25.7`），需同步确认团队工具链可用，或在权衡后锁定不抬高 Go 版本的依赖版本。
- 国内网络可用镜像代理：`GOPROXY=https://goproxy.cn,https://goproxy.io`。
- 提交前用 `go mod verify` 校验依赖完整性。


## 编码规范

> 完整、可执行的强制约束见 [`docs/开发规范.md`](docs/开发规范.md)，下面是要点摘录。


## 当前应用入口

当前项目内置了：

- `GET /`：返回 `ThinkGo`。
- `GET /api/users`：调度到 `User@Index`，返回 `User List`。
- 全局 `RequestID` 中间件：读取或生成 `X-Request-ID`，写入请求上下文并透传到响应头。
