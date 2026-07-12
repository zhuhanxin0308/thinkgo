# ThinkGo

ThinkGo 是一个参考 ThinkPHP 使用习惯实现的 Go Web 框架。框架保留控制器、模型、视图、路由、中间件、配置、事件、验证、缓存、会话、日志和命令行等核心概念，同时按 Go 的长期运行进程、显式依赖和并发安全方式实现。

## 环境要求

- Go 版本以 `go.mod` 为准，当前最低为 `go 1.26.5`，包含 `crypto/tls`、`html/template`、`net/http`、`os.Root` 等标准库安全修复。本机 Go 低于该版本时，`GOTOOLCHAIN=auto`（默认）会自动拉取匹配工具链。
- 运行 Web 服务前必须保证 `config/database.json` 或 `.env` 中的数据库配置可用。
- 如启用 TLS 或 HTTP/3，证书文件必须存在，默认读取 `runtime/cert.pem` 和 `runtime/key.pem`。
- 依赖统一通过 Go Modules 管理，详见[依赖管理](#依赖管理)。完整开发约束见 [`docs/开发规范.md`](docs/开发规范.md)。
- 按章节组织的框架文档见 [`docs/README.md`](docs/README.md)。

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
│   ├── think/           # 命令行入口
│   └── cert/            # 本地开发证书工具
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
go run cmd/think/main.go run            # 使用配置/默认端口启动
go run cmd/think/main.go run -p 9000    # 指定监听端口（-p 或 --port），覆盖配置与 .env
```

`run` 命令会自动探测 [air](https://github.com/air-verse/air)：已安装时以热重载方式启动（修改代码自动重新构建），未安装时通过 `App.Run()` 以普通模式启动并提示安装方法。初始化或 Provider 启动失败时不会继续监听端口，退出时统一关闭应用资源。两种模式下 `-p` 均生效——指定的端口通过环境变量传递给热重载子进程，且优先级高于 `.env` 中的 `SERVER_PORT`。热重载模式会自动准备 `bin` 目录并输出 `bin/server` 或 `bin/server.exe`。

Windows 交叉构建 Linux amd64 可运行 `build_linux.bat`，产物为 `bin/thinkgo-linux-amd64-nocgo`。该产物明确关闭 CGO，不包含 SQLite 驱动能力；完整 SQLite 构建必须在具备 C 工具链的目标环境中启用 CGO。

每次修改代码后必须运行：

```bash
go test ./...
```

## 入口规范

HTTP 入口必须导入控制器、中间件和路由包，触发各包的 `init` 自动注册。

```go
package main

import (
	"fmt"
	"os"

	_ "thinkgo/app/controller" // 注册控制器
	_ "thinkgo/app/middleware" // 注册全局中间件
	"thinkgo/framework"
	"thinkgo/framework/http"
	_ "thinkgo/route" // 注册路由
)

func main() {
	app := framework.NewApp()
	kernel, err := http.NewHttp(app)
	if err != nil {
		_ = app.Close()
		fmt.Fprintln(os.Stderr, "HTTP kernel initialization failed:", err)
		os.Exit(1)
	}
	app.Kernel = kernel
	if err := app.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "Application exited with error:", err)
		os.Exit(1)
	}
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

`Get` 和 `GetMap` 返回递归隔离的 map/slice 快照；修改返回值不会影响共享配置。共享配置写入必须使用 `Set`。

配置优先级（从高到低）：

1. 系统环境变量（真实进程环境变量，最高优先级）。
2. `.env` 文件。
3. `config/*.json`。
4. 框架默认值。

环境变量遵循 12-factor 约定：**真实系统环境变量优先于 `.env` 文件**，便于容器/CI/命令行（如 `run -p` 注入的 `SERVER_PORT`）覆盖仓库里的 `.env` 默认值；`.env` 仅作为本地兜底。框架加载 `.env` 时只写入内部存储，不会调用 `os.Setenv` 污染进程环境（避免 `DB_PASS` 等敏感值被子进程继承）。

`.env` 缺失时继续使用系统环境变量和默认配置；其他读取或语法错误会中止加载且包含文件、行号。布尔变量通过 `Env.GetBool` 读取并检查错误，非法值不会静默使用默认值。

> 注意：环境变量覆盖仅对框架显式桥接的键生效（`APP_DEBUG`、`APP_TRACE`、`SERVER_*`、`DB_*` 等）。其余配置项只走 `config/*.json` 与默认值。

常用环境变量：

```text
APP_ENV=production
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

路由必须集中定义在 `route` 包，并通过 `framework.MustRegisterRouteLoader` 注册可返回错误的加载函数。

```go
package route

import (
	"thinkgo/framework"
	"thinkgo/framework/context"
)

func init() {
	framework.MustRegisterRouteLoader(Load)
}

func Load(app *framework.App) error {
	home, err := app.Route.Get("/", func(req *context.Request) *context.Response {
		return context.NewResponse().Content("ThinkGo")
	})
	if err != nil {
		return err
	}
	if err = home.WithName("home"); err != nil {
		return err
	}

	users, err := app.Route.Get("/api/users", "User@Index")
	if err != nil {
		return err
	}
	return users.WithName("users.index")
}
```

路由注册返回 `(*route.Route, error)`，路由配置方法返回 `error`；加载器必须逐项处理。HTTP 内核启动或首次匹配时会冻结路由，之后的修改返回 `route.ErrRouterFrozen`。

支持的路由能力：

- `Get`、`Post`、`Put`、`Delete`、`Patch`、`Options`、`Head`、`Any`。
- `Group`、`Domain`、`DomainGroup` 的回调接收隔离的 `*route.Group` 并返回错误。
- `Resource` 返回 `(*ResourceRoute, error)`，`Only` 和 `Except` 返回错误。
- `WithName` 命名路由。
- `WithPattern` 参数正则。
- `WithExtension` 后缀约束；`WithDomain` 设置单路由域名。
- `Miss(handler)` 兜底路由。
- `Redirect(path, target, code...)` 重定向路由，状态码仅支持 `301`、`302`、`303`、`307`、`308`。

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

- `url_route_must`：`true`（默认，**安全基线**）表示只允许显式注册的路由；`false` 时开启只读自动路由——未命中显式路由的 GET/HEAD URL 会按 `/控制器/动作` 自动解析为 `Controller@Action`。
- **安全提示**：自动路由会让业务控制器导出的只读动作成为可达端点（框架已屏蔽基类内置方法，写方法返回 405）。仅在受控场景开启。
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
	framework.MustRegisterController("User", &User{})
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

HTTP 内核会在业务逻辑前调用 `req.Parse()`：重复或尾随 JSON、非法表单返回 400，请求体超限返回 413。独立构造请求使用 `context.NewRequest(raw, options...) (*Request, error)`，结束时处理 `Cleanup() error`。

参数优先级：

```text
路由参数 > 表单参数 > JSON 请求体 > 查询参数
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

直接调用 `Response.Send(writer)` 时必须处理返回的 `error`。响应头控制字符会被整体拒绝，`Content-Length`、`Transfer-Encoding` 等分帧字段由 HTTP 内核管理。JSON/XML/JSONP 序列化失败会固定返回通用 500，不能被后续链式实体覆盖。

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
	framework.MustRegisterGlobalMiddleware(Auth)
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
_, err := app.Route.Get("/api/profile", "User@Profile", middleware.Auth)
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

数据库配置在 `config/database.json`。`default` 必须引用非空 `connections` 中已声明的连接；未知字段、错误类型、非法范围或连接失败会形成启动错误，不会创建隐式回退连接。默认连接通过 `app.DB` 使用，多连接通过 `app.DBManager.Connection(name)` 获取；`DBManager.Default()` 返回 `(*db.DB, error)`。

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
- 插入、更新和批量写入会复制调用方 `map`，自动时间戳、模型回调与主键回填不会污染输入。

关联查询（JOIN）会对 JOIN 表套用与主表一致的前缀规则：`Name()` 创建的查询给 JOIN 表追加前缀，`Table()` 创建的查询按完整表名处理。JOIN 条件仅支持安全的 `字段 op 字段` 形式（支持 `table.field` 与 `AND` 连接），紧凑写法与带空格写法均可。未调用 `Field` 时只返回主表的 `主表.*`，避免关联表同名列覆盖；需要关联表字段时必须显式选择，并为同名列设置唯一别名。

事务用法：

```go
err := app.DB.Transaction(func(tx *db.Tx) error {
	if _, err := tx.Table("users").Insert(data); err != nil {
		return err
	}
	return nil
})
```

闭包业务错误与回滚错误会通过 `errors.Join` 同时保留；panic 会先回滚再原样抛出。事务提交或回滚后再次操作返回 `ErrTransactionDone`，不会退回普通连接。数据库关闭会等待活动事务和查询释放连接租约。

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

> 说明：内置 SQL、MongoDB 和 Neo4j 连接都实现了 context 接口；MongoDB/Neo4j 还提供默认 10 秒操作上限。传入 `nil` context 会返回 `ErrInvalidQuery`。

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
- `ChunkById(count, pk, cb)`：基于主键游标（`WHERE id > ? ORDER BY id LIMIT count`）的分块遍历，**推荐用于大表全量遍历**，无 OFFSET 性能问题。满批结果必须包含类型稳定且严格递增的游标，否则返回 `ErrInvalidDatabaseRow`。

```go
err := app.DB.Name("users").ChunkById(1000, "id", func(rows []map[string]interface{}) bool {
	// 处理本批数据；返回 false 可提前终止。
	return true
})
```

### 批量插入

`InsertAll` 在占位符总数（行数 × 列数）超过当前方言上限时自动分批；多批写入会包裹在事务中以保证原子性：

```go
affected, err := app.DB.Name("users").InsertAll([]map[string]interface{}{
	{"name": "a", "age": 1},
	{"name": "b", "age": 2},
})
```

要求所有行的字段集合一致，否则报错（避免错位写入脏数据）。多批操作任一批次失败时会回滚，并同时保留执行与回滚错误。

### 悲观锁

`Lock(true)` 排他锁、`Lock(false)` 共享锁。框架按方言翻译锁子句：

| 方言 | 排他锁 | 共享锁 |
|---|---|---|
| MySQL | `FOR UPDATE` | `LOCK IN SHARE MODE` |
| PostgreSQL | `FOR UPDATE` | `FOR SHARE` |
| SQLite | 不输出（无行级锁） | 不输出 |

### 多方言与 PostgreSQL 注意事项

- 框架统一以 `?` 占位符构建 SQL，执行前按方言转换（MySQL/SQLite `?`、PostgreSQL `$N`、SQL Server `@pN`、Oracle `:N`）；PostgreSQL 单字符 JSONB `?` 操作符在框架 SQL 中写作 `??`，`?|`、`?&`、`@?` 可直接使用。
- 分页语法自动适配：MySQL/PostgreSQL/SQLite 用 `LIMIT/OFFSET`，SQL Server/Oracle 用 `OFFSET … FETCH`（无显式排序时 SQL Server 会补默认排序以满足语法）。
- **PostgreSQL 自增主键回传**：`lib/pq` 不支持 `LastInsertId()`，框架自动改用 `INSERT … RETURNING <primary_key>`；非 `id` 主键必须在模型上调用 `PrimaryKey("实际主键")`。

### 错误日志脱敏

数据库错误日志只记录字段名、SQL 模板与参数数量，**不记录字段值与绑定参数值**；原生 SQL 和 `WhereRaw` 中的字符串字面量、PostgreSQL dollar quote、行注释与块注释内容会先脱敏再写日志，避免密码、令牌、个人信息等泄露。

### 表达式条件 `WhereExp` 的安全边界

`WhereExp(field, op, expr)` 只接受“列名 + 白名单函数（如 `NOW()`、`COUNT()`）+ 算术运算 + `?` 占位符”构成的安全表达式，禁止引号字符串、分号、注释符及 `SELECT/UNION/OR/AND` 等关键字（去空格写法也会被识别拒绝）。需要更复杂的原始表达式时请改用 `WhereRaw` 并自行保证参数化。

### 性能调优（可选）

所有 SQL 连接统一应用连接池配置并在返回前执行有界 `PingContext`。MySQL 默认启用 `tls=true`；如需减少每条查询的预编译往返，可在受信配置的字符串参数中设置 `interpolateParams=true`。高风险参数 `multiStatements`、`allowAllFiles`、`allowCleartextPasswords`、`allowFallbackToPlaintext` 的 `true` 值会被拒绝。

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
- `SoftDelete`、`WithTrashed`、`OnlyTrashed`、`Restore`、`ForceDelete`；更新、删除和恢复均要求业务 WHERE，自动软删除条件不能绕过全表保护。
- `Getter`、`Setter`、`Searcher`（注册方法返回 `error`）。
- `On` 注册模型事件并返回 `error`。
- `DefineHasOne`、`DefineHasMany`、`DefineBelongsTo`、`DefineBelongsToMany` 均返回 `error`；预加载按强类型键关联，非默认主键请用 `PrimaryKey` 显式声明。
- `WithContext` 绑定请求上下文；`ChunkById` 主键游标遍历；`Paginate` 对 JOIN/GROUP/DISTINCT 统计正确的 total。
- 终端方法（`Find`/`Count`/`Select` 等）在克隆上执行，同一查询对象可安全重复使用。

表命名规范：

- `db.NewModelAuto(database, value)` 返回 `(*db.Model, error)`，只把具名结构体名转为 `snake_case`。
- 不会自动复数化。
- 表前缀由数据库配置统一追加。
- 实际表名不匹配时必须使用 `db.NewModel(database, "actual_table")`。

## 验证器用法

验证器放在 `app/validate`，规则使用 ThinkPHP 风格的字符串组合。

```go
v := validate.NewValidator().SetRules(map[string]string{
	"email|邮箱": "required|email",
	"age|年龄":   "integer|min:1",
}).SetMessages(map[string]string{
	"email.required": "邮箱不能为空",
})

result, err := v.Validate(req.All())
if err != nil {
	return c.Error("验证配置错误", 500)
}
if !result.Valid() {
	return c.Error(result.FirstError(), 422)
}
```

支持场景：

```go
v.SetScenes(map[string][]string{
	"create": {"email", "age"},
})

result, err := v.Validate(data, validate.WithScene("create"), validate.CollectAllErrors())
```

`Validate` 返回独立结果和配置错误，同一验证器可并发复用。常用规则包括 `required`、`number`、`integer`、`float`、`boolean`、`email`、`array`、`accepted`、`date`、`alpha`、`alphaNum`、`alphaDash`、`chs`、`ip`、`url`、`in`、`between`、`length`、`max`、`min`、`eq`、`gt`、`lt`、`regex`、`confirm`、`different`、`mobile`、`dateFormat`、`after`、`before`、`requireIf`、`requireWith`、`idCard`。完整规则语义见[验证器](docs/验证/验证器.md)。

## 视图用法

视图默认使用 Go 模板驱动。`config/view.json` 仅接受 `view_path`、`view_suffix` 和 `cache`；模板真实路径必须位于根目录内，单文件不超过 4 MiB。

```go
func (c *User) Page() string {
	c.Assign("title", "用户列表")
	return c.Fetch("user/index")
}
```

模板中可使用框架注册的 `lang` 函数读取多语言内容。

`SetDriver`、`SetFuncMap`、`Render`、`Fetch` 和 `Exists` 都会返回错误；`Exists` 的签名为 `(bool, error)`。多语言检测参数、Cookie、头名、允许列表和浏览器检测开关均由 `config/lang.json` 生效，`Accept-Language` 遵守 `q` 权重。

## 缓存用法

缓存配置在 `config/cache.json`。`default` 必须引用已定义的 store；支持 `file`、`memory`、`redis`，未知字段、类型错误和非法范围会阻止启动，不会回退到文件驱动。

```go
if err := app.Cache.Set("user:1", user, time.Minute); err != nil {
	return err
}
value, found, err := app.Cache.Get("user:1")
if err != nil {
	return err
}
if found {
	// 命中的 value 允许为 nil。
}

tagged, err := app.Cache.Tag("user")
if err != nil {
	return err
}
if err := tagged.Set("user:1", user, time.Minute); err != nil {
	return err
}

lock, err := app.Cache.Lock("sync:user", 10*time.Second)
if err != nil {
	return err
}
acquired, err := lock.Acquire()
if err != nil {
	return err
}
if acquired {
	defer func() { _, _ = lock.Release() }()
}
```

`Set`、`Has`、`Forget`、`Forever`、`Flush` 均返回 `error`；`Get` 返回 `(value, found, error)`；`Inc/Dec` 返回 `(int64, error)`；`Store`、`Tag`、`Lock` 也必须先处理创建错误。`Remember` 的加载函数签名是 `func() (interface{}, error)`，同一进程内相同 store/key 的并发未命中会合并为一次加载。

Memory 保留 Go 动态类型；File、Redis、DB 使用 JSON，读取后的 JSON 数值为 `float64`。文件缓存使用哈希文件名、16 MiB 单项上限和原子替换；`Flush` 保留锁与非缓存文件。应用关闭时会等待活动缓存操作并关闭 Redis 等可关闭驱动。

Redis store 应配置非空 `prefix`，使 `Flush()` 只清理本应用业务键并保留活动锁。空前缀默认拒绝清理；只有独占 DB 才可显式开启 `allow_flush_db`，此时 `Flush()` 会执行 `FLUSHDB`：

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
      "timeout_ms": 3000,
      "allow_flush_db": false
    }
  }
}
```

完整 API、标签一致性边界和驱动说明见[缓存文档](docs/缓存/缓存.md)。

## Cookie 与 Session

Cookie 配置在 `config/cookie.json`，Session 配置在 `config/session.json`，CSRF 配置在 `config/csrf.json`。三个模块都会拒绝未知字段、错误类型和越界数值；配置错误会阻止应用启动。

启用 Session：

```json
{
  "session_enable": true
}
```

请求级 Session API 会显式返回错误：

```go
requestSession, ok := req.GetData("_session").(*session.Session)
if !ok {
    return context.NewResponse().Abort(500, "会话不可用")
}
if err := requestSession.Set("user_id", userID); err != nil {
    return context.NewResponse().Abort(500, "会话写入失败")
}
value, found := requestSession.Get("user_id")
```

登录或提权后必须处理 `Regenerate()` 错误，退出登录必须处理 `Destroy()` 错误。文件 Session 使用 `storage_path`，Cookie 路径使用 `cookie_path`，不再支持含义歧义的旧 `path` 字段。控制器不要自行解析 Session 文件。

## 事件用法

事件调度器通过 `app.Event` 使用。

```go
type UserCreatedListener struct{}

func (l *UserCreatedListener) Handle(e event.Event) error {
	// 处理用户创建事件。
	return nil
}

if err := app.Event.Listen("user.created", &UserCreatedListener{}); err != nil {
	return err
}
if err := app.Event.Dispatch(event.NewEvent("user.created", userID)); err != nil {
	return err
}
```

事件名、监听器、订阅者和优先级会在注册时校验；监听器返回的业务错误与回调 panic 会由 `Dispatch` 返回，不会静默丢失或直接击穿进程。

框架内置生命周期事件：

- `framework.AppInit`
- `framework.HttpRun`
- `framework.HttpEnd`
- `framework.RouteLoaded`

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

日志器支持多驱动、多通道、级别过滤、调用位置记录、异步批量写入和关闭时刷盘。需要完成屏障时调用 `Flush(ctx)`；绕过 `app.Run()` 时必须调用 `app.Close()` 并处理 Provider、缓存、数据库和日志的聚合关闭错误。文件日志默认限制保留天数、文件数和总容量，详细配置见[日志](docs/日志/日志.md)。

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

`run` 命令支持 `-p`/`--port` 指定监听端口（覆盖配置与 `.env`，详见[启动方式](#启动方式)）。应用启动检查通过后才写入环境和内存配置，失败不会污染端口状态。`clear` 先清理当前缓存后端；失败时停止，成功后安全清理 `runtime/cache` 中受管的缓存项，保留活动锁和非缓存文件，也不会删除日志、会话、证书或离线数据库文件。除 `run` 外，其余命令（`version`、`list`、`make:*` 等）不需要数据库，命令行入口对它们使用 `framework.NewConsoleApp()` 跳过连库，避免每次执行都尝试连接数据库并打印连接错误。

参数解析会拒绝未知、重复、缺值和数量不匹配的输入。`config:dump` 对完整配置、直接敏感点路径、强类型映射和结构体统一脱敏。生成器在应用根目录内格式化并独占创建源码，不覆盖已有文件。命令注册、解析、执行、输出和应用关闭错误都会让入口以非零状态退出。

本地缺少 TLS 证书时运行 `go run cmd/cert/main.go`。工具生成 ECDSA P-256 开发证书并拒绝覆盖任一已有文件；类 Unix 私钥权限为 `0600`，Windows 使用仅当前用户可访问的受保护 DACL。

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

func (c *DemoCommand) Execute(input *console.Input, output *console.Output) error {
	name := input.GetArgument(0)      // 位置参数（已剔除选项）
	dir := input.GetOption("output")  // 取值选项，未传时返回默认值 ./dist
	force := input.GetOption("force") // 布尔开关出现时为 "true"
	output.Writeln(name + " -> " + dir + " force=" + force)
	return output.Err()
}
```

`ICommand.Execute`、`Console.Register` 和 `Console.Run` 都返回 `error`，调用方必须处理。选项写法支持 `--name value`、`--name=value`、短选项 `-n value`、`-n=value` 以及布尔开关 `--flag`；短选项会按声明解析为规范长名。错误状态写入 stderr；颜色仅在交互式终端启用，状态消息中的控制字符会被转义。

## 依赖管理

- 依赖统一使用 Go Modules，不提交 `vendor/`。新增依赖后必须运行 `go mod tidy` 保持 `go.mod`/`go.sum` 干净。
- 升级依赖：

  ```bash
  go get -u ./...   # 同主版本内升到最新 minor/patch
  go mod tidy
  go build ./... && go test ./...
  ```

- 跨大版本升级（如 `module/v2`）属破坏性变更，必须单独评估并配套改造代码与测试，不在常规升级内顺带进行。
- 升级后若依赖抬高了 `go` 指令最低版本，需同步确认团队工具链可用；标准库安全公告要求更高补丁版本时不得继续锁定存在已知漏洞的旧工具链。
- 国内网络可用镜像代理：`GOPROXY=https://goproxy.cn,https://goproxy.io`。
- 提交前用 `go mod verify` 校验依赖完整性。
- 提交前用 `go mod tidy -diff` 校验模块清单无漂移，并运行固定版本 `go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...`。仓库 CI 同时覆盖 Linux CGO/Race、Oracle 标签、Windows 平台行为和核心覆盖率。

## 测试规范

- 新功能、修复和重构必须先写有意义的测试。
- 测试必须验证行为，不要只追求覆盖率。
- 涉及文件、缓存、数据库、会话等 IO 的测试必须清理数据。
- 框架核心逻辑测试放在对应 `framework` 子包。
- 应用集成测试可放在项目根目录，验证入口、注册链路和实际 HTTP 行为。
- 提交前必须运行 `go test ./...`。

当前应用层集成测试验证：

- `app/controller` 控制器注册。
- `app/middleware` 全局中间件注册。
- `route` 路由加载器注册。
- HTTP 内核可以通过 `/api/users` 调度到 `User@Index`。

## 编码规范

> 完整、可执行的强制约束见 [`docs/开发规范.md`](docs/开发规范.md)，下面是要点摘录。

- 代码注释必须使用中文。
- 不允许空实现、占位实现或只有注释没有行为的代码。
- 不允许通过放宽类型检查、放宽规则或降低测试质量来规避问题。
- 不允许把请求级状态写入全局变量。
- 不允许新增无意义 demo 文件；示例必须能运行或由测试覆盖。
- 不允许批量脚本式修改代码，修改必须逐文件进行。
- 优先复用框架已有组件、配置、容器、日志、事件和中间件能力。
- 数据库写操作必须显式处理错误，行集迭代后必须检查 `rows.Err()`。
- 用户输入进入 SQL 时必须参数化；表名/字段名/操作符必须走白名单校验。
- 读取配置用 `GetBool`/`GetInt`/`GetString`/`GetMap`，禁止对裸 `Get` 返回值做类型断言（会在配置异常时启动崩溃）。
- 共享可变状态（map、单例、计数器）必须加锁；请求级状态用 `req.Set`/`req.GetData` 传递。
- 控制器、路由、中间件、验证器、模型的文件命名使用小写蛇形。
- Go 导出类型和方法使用 PascalCase，非导出标识使用 camelCase。
- 业务错误应返回结构化响应，内部错误在生产环境不要暴露敏感细节。

## 当前应用入口

当前项目内置了：

- `GET /`：返回 `ThinkGo`。
- `GET /api/users`：调度到 `User@Index`，返回 `User List`。
- 全局 `RequestID` 中间件：读取或生成 `X-Request-ID`，写入请求上下文并透传到响应头。
