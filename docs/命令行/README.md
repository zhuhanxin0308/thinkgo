# 命令行

ThinkGo 命令行入口是 `cmd/think/main.go`，底层命令系统位于 `framework/console`。命令按“声明参数 -> 严格解析 -> 执行 -> 返回错误”的流程运行。

命令由 `ApplicationManager` 管理全部应用。支持应用级操作的命令声明 `--app/-a`，未指定时使用 `default_app` 或 `index`：

```bash
go run cmd/think/main.go route:list --app admin
go run cmd/think/main.go make:controller Account --app admin
```

`run` 的 `--app` 不会把 HTTP 服务缩减为单个应用；服务仍由一个 `MultiHttp` 监听器承载全部应用。

本目录按能力拆分：

- [命令系统](命令系统.md)：`ICommand`、注册、帮助和应用注入。
- [输入解析](输入解析.md)：位置参数、长短选项、布尔值和错误边界。
- [输出与错误](输出与错误.md)：stdout/stderr、颜色、控制字符和退出码。
- [内置命令](内置命令.md)：list、version、route:list、run、clear 和 config:dump。
- [代码生成命令](代码生成命令.md)：make:* 命令、名称规范和安全写入。
- [启动服务](启动服务.md)：端口覆盖、普通模式和 Air 热重载。

常规运行方式：

```bash
go run cmd/think/main.go list
```

当前 WSL 工作区使用的 Go 工具链路径是 `$HOME/.local/go1.26.5/bin`；当 `go` 不在 PATH 中时，可把示例中的 `go` 替换为 `$HOME/.local/go1.26.5/bin/go`。
