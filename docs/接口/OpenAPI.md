# OpenAPI 契约

`framework/openapi` 使用完整的 OpenAPI 3.1 数据模型和校验器。注册表要求每个操作具有唯一 `operationId` 和至少一个响应，首次导出后冻结，调用方在注册后修改原始结构不会改变最终文档。

## 一次注册处理器与契约

### 从普通 Go 注释生成

首次在项目根目录执行 `go run ./cmd/think openapi:generate`，再引入生成的 `internal/apidoc` 包。把创建注册表的调用换为 `apidoc.NewRegistry(info)`，后续接口只需编写具名处理器、请求与响应类型，以及普通 Go 注释：

```go
// ShowUserInput 用户查询条件。
type ShowUserInput struct {
	binding.Input
	// ID 用户编号。
	ID int64 `path:"id" json:"-" validate:"gt:0"`
}

// UserOutput 用户公开资料。
type UserOutput struct {
	ID int64 `json:"id"` // 用户编号。
	// Name 用户显示名称。
	Name string `json:"name"`
}

// ShowUser 查询用户。
//
// 返回用户的公开资料；用户不存在时返回 404。
//
// @Tags 用户
func ShowUser(input ShowUserInput, service *UserService, request *framework.Request) (UserOutput, error) {
	return service.Show(request.Context(), input.ID)
}
```

在应用的路由加载器中使用同一注册表：

```go
registry, err := apidoc.NewRegistry(openapi3.Info{Title: "Account API", Version: "1.0.0"})
if err != nil {
	return err
}
routes := registry.Routes(app.Route())
if err := routes.Get("/api/users/:id", ShowUser); err != nil {
	return err
}
```

`apidoc` 的导入路径为 `<项目模块路径>/internal/apidoc`。`Routes` 也接受已有路由分组；`Get`、`Post`、`Put`、`Patch`、`Delete`、`Head`、`Options` 保留原有注入和中间件能力。需要 201 等成功状态或自定义元信息时，用 `routes.Handle(openapi.Operation{Method: http.MethodPost, Path: "/api/users", SuccessStatus: http.StatusCreated}, CreateUser)`。

普通注释首行是接口标题，其余正文是说明，支持 Markdown；开头的函数名会自动去除。类型注释成为 Schema 说明，字段前置或行尾注释成为参数与属性说明。嵌入字段、嵌套对象和泛型实例使用对应声明的说明，匿名对象的字段说明按声明位置隔离。同模块的派生结构体继承原字段说明；类型别名在运行时采用实际类型的说明。字段的显式 `doc:"说明"` 标签优先。

只在需要补充信息时使用以下注解，大小写敏感：

| 注解 | 用途 |
|---|---|
| `@Tags 用户, 后台` | 接口分组；含逗号的名称可以用双引号包围 |
| `@ID users.show` | 显式客户端操作名；省略时由完整处理器符号、HTTP 方法和最终分组路径生成稳定标识 |
| `@Deprecated 请使用新接口` | 标记弃用，原因可省略 |
| `@Summary 查询用户` | 覆盖普通首行标题 |
| `@Description 补充说明` | 追加说明，可多次使用 |

函数注释中的未知注解、重复的单值注解及空内容会报告源码文件和行号。路由、字段来源、验证规则、响应类型与成功状态继续由真实代码决定，无需 `@Router`、`@Param` 或 `@Success`。注释只描述行为，404 等业务错误仍由处理器实际返回，鉴权仍由应用配置和中间件执行。

显式 `Operation` 的非空标题、说明、操作名和非 nil 分组优先；弃用标记取代码与注释的并集。自动关联要求处理器与 DTO 定义在当前模块可导入的业务包中，例如 `app/index/api`。匿名函数、`main` 包及模块外的处理器可显式提供 `OperationID` 和 `Summary`。未启用源码注释的 `openapi.NewRegistry` 保持原有显式契约行为。

### 自动生成与构建检查

```bash
# 首次接入，或直接更新
go run ./cmd/think openapi:generate

# 生成文件中已包含指令，可纳入本地与 CI 构建流程
go generate ./internal/apidoc

# 只读检查，缺失或过期时以非零状态退出
go run ./cmd/think openapi:generate --check
```

命令根据当前模块的 Go 构建文件提取注释，尊重 `GOOS`、`GOARCH`、`CGO_ENABLED` 和 `GOFLAGS` 构建标签，排除测试文件、嵌套模块与依赖源码。生成与部署应使用相同构建条件。该命令不启动业务 Provider、不连接数据库，也不修改项目的 `go.mod` 或 `go.sum`。

生成的 `comments_generated.go` 和 `comments_generated.json` 应一并提交；它们是编译期注释元数据，最终 OpenAPI JSON 仍由真实路由注册后的 `registry.JSON` 或 `registry.Handler` 导出。元数据通过 `go:embed` 进入程序，生产环境无需源码。修改注释后需重新生成并编译；`go build` 本身不会运行 `go generate`。

启用后，`service:discover`、会刷新发现清单的生成命令，以及应用脚手架命令 `build` 会在同一写入事务内更新注释；`CheckControllerDiscovery` 同时检查注释产物是否过期。未启用的项目不会额外生成文件。生成失败保留旧产物，首次生成不会覆盖同名用户文件。

### 显式元信息

业务 JSON API 使用 `openapi.Handle`。输入结构体嵌入 `binding.Input`，字段声明来源与验证规则；框架从真实函数签名推导输入输出，保留请求、服务、模型注入，无需另外声明泛型文档类型。

```go
type ShowUserInput struct {
	binding.Input
	ID int64 `path:"id" validate:"gt:0"`
}

type UserOutput struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

err := openapi.Handle(router, registry, openapi.Operation{
	Method:      http.MethodGet,
	Path:        "/api/users/:id",
	OperationID: "users.show",
	Summary:     "查询用户",
	Tags:        []string{"用户"},
}, func(input ShowUserInput, service *UserService, request *framework.Request) (UserOutput, error) {
	return service.Show(request.Context(), input.ID)
})
```

`router` 可使用 `*route.Router`、`*route.Group` 或现有 `app.Route()` 门面，分组前缀与中间件均保留。处理器支持具体数据返回、`(数据, error)`、`error` 和无返回值。`SuccessStatus` 默认按签名选择：数据返回为 200，空返回为 204；创建接口可设为 `http.StatusCreated`。数据统一编码为 JSON，包括字符串和空指针。错误继续进入框架异常处理，不会被成功状态码覆盖。

路径变量必须与请求结构体的 `path` 字段一一对应。JSON 接口完整匹配声明路径，不自动追加框架默认 URL 后缀，避免列表路径吞掉详情路径。动态顶层 `any`、原始 `*Response`、独立标量参数、可变参数，以及为 204/205 声明响应体会在注册时返回错误。流式或自定义响应使用普通路由和显式文档注册。域名分组与可选路径需要单独建模，不能直接通过此入口注册。

任一步骤失败都不会占用新路由、操作名或组件。`Summary`、`Description`、`Tags`、`Deprecated`、`Security` 可描述业务元信息。注册后修改原始元信息不会影响文档。

## 显式注册与导出

需要自定义协议或手工契约时，仍可使用 `Register`；`RegisterTyped` 保留为只注册文档的类型化入口。以下示例中的实际业务路由应先行注册。

```go
registry, err := openapi.NewRegistry(openapi3.Info{
	Title:   "Account API",
	Version: "1.0.0",
})
if err != nil {
	return err
}
operation := &openapi3.Operation{
	OperationID: "users.show",
	Parameters: openapi3.Parameters{
		&openapi3.ParameterRef{
			Value: openapi3.NewPathParameter("id").WithSchema(openapi3.NewStringSchema()),
		},
	},
	Responses: openapi3.NewResponses(
		openapi3.WithStatus(http.StatusOK, &openapi3.ResponseRef{
			Value: openapi3.NewResponse().WithDescription("成功"),
		}),
	),
}
if err := registry.Register(http.MethodGet, "/api/users/:id", operation); err != nil {
	return err
}
handler, err := registry.Handler(context.Background())
if err != nil {
	return err
}
if _, err := router.Get("/openapi.json", handler); err != nil {
	return err
}
if err := registry.ValidateRouter(router, "/api"); err != nil {
	return err
}
```

ThinkGo 的 `:id` 会转换为 OpenAPI 的 `{id}`。OpenAPI 不支持可选路径段，`:id?` 必须拆为两条明确的实际路由和两条契约，避免生成的客户端无法判断路径形态。

`ValidateRouter` 会冻结路由，因此须在全部路由注册完成后调用。它对指定前缀做双向核对：实际路由缺少契约、契约缺少实际路由、ANY 路由以及不可表达的可选路由都会失败。健康检查等不属于公开 API 的路径应放在前缀外；确有需要时可通过 `ignored` 参数用 `METHOD /path` 精确排除。

文档处理器只允许 GET/HEAD，返回不可变 JSON、SHA-256 ETag、`nosniff` 和条件请求 304。生产环境是否公开 `/openapi.json` 仍应由路由鉴权和部署策略决定。

## 接口测试

`testkit.New(t, options)` 自动创建隔离配置和临时目录，启动真实应用、路由、中间件及请求作用域，并注册测试结束时的资源清理。`Options.Register` 在初始化前注册模型和路由加载器，`Options.Configure` 在初始化后、Provider 启动前替换依赖；`Options.Database` 接收独立测试数据库并交由宿主关闭。

```go
host := testkit.New(t, testkit.Options{
	Database: database,
	Register: registerApplication,
})
response, err := host.JSON(http.MethodPost, "/api/users", map[string]any{"name": "Ada"})
if err != nil || response.Code != http.StatusCreated {
	t.Fatalf("创建失败: %v %v", response, err)
}
user, err := testkit.DecodeJSON[UserOutput](response)
if err != nil {
	t.Fatal(err)
}
```

自定义上下文、Cookie 或请求头时，构造标准库请求并调用 `host.Do(request)`。`DecodeJSON[T]` 校验媒体类型和单个 JSON 值，保留原始响应体；动态数字使用 `json.Number`。完整 SQLite 示例见仓库的 `tests/typed_api_integration_test.go`。
