# 命令行

框架根目录的 `cmd/thinkgo` 是可安装 CLI，命令系统位于 `console/`。使用 `go install github.com/zhuhanxin0308/thinkgo/v3/cmd/thinkgo@v3.0.1` 安装，再用 `thinkgo create <项目名称>` 创建完整项目。目标目录必须不存在或为空。

以下命令在生成的业务项目目录运行，项目 `cmd/think` 与已安装的 `thinkgo` 共用命令实现。CLI 与 HTTP 共享同一份编译期应用清单；应用级命令通过 `--app/-a` 选择独立 `framework.App`，省略时使用项目 `app.default_app`。

```bash
go run ./cmd/think list
go run ./cmd/think route:list --app admin
go run ./cmd/think make:controller Account --app admin
go run ./cmd/think deploy:check --strict
```

本目录按能力拆分：

- [命令系统](命令系统.md)：自定义命令、注册、项目 App 注入和应用选择。
- [输入解析](输入解析.md)：位置参数、长短选项、布尔值和错误边界。
- [输出与错误](输出与错误.md)：stdout、stderr、颜色、控制字符和退出码。
- [内置命令](内置命令.md)：多应用默认命令与项目扩展命令。
- [代码生成命令](代码生成命令.md)：`make:*` 的应用选择、目录和自动发现。
- [启动服务](启动服务.md)：原生多应用宿主、端口覆盖和 Air 热重载。

命令遵循“声明参数、严格解析、选择应用、执行、返回错误”的固定流程。初始化失败、未知应用、参数错误和输出错误都会产生非零退出码。
