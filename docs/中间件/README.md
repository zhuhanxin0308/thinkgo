# 中间件

本目录按实际实现拆分 ThinkGo 的中间件能力。跨应用中间件在根 `app/middleware.go` 声明；业务应用自己的实现放在 `app/<name>/middleware`，并由 `app/<name>/middleware.go` 声明。两层都由静态应用清单装配，开发者不需要接触框架注册表。

- [中间件概览](中间件.md)：处理器签名、装配顺序和边界。
- [管道与生命周期](管道与生命周期.md)：`Pipeline`、别名、优先级和 `terminate`。
- [全局中间件](全局中间件.md)：应用包注册表、请求入口和全局中间件写法。
- [路由中间件](路由中间件.md)：路由、分组继承、冻结和移除。
- [控制器中间件](控制器中间件.md)：别名声明、动作过滤和失败关闭。
- [请求编号](请求编号.md)：按业务需要实现 `X-Request-ID` 中间件。
- [语言中间件](语言中间件.md)：请求语言来源优先级和请求上下文。
- [Recovery](Recovery.md)：panic 恢复和异常响应转换。
- [Session](Session.md)：请求级会话初始化、保存和响应头提交。
- [Trace](Trace.md)：本机调试面板、资源端点和输出门禁。
- [CSRF](CSRF.md)：签名双重提交 Cookie、防重放和配置校验。
- [CORS](CORS.md)：通用跨域中间件的来源、预检和凭证行为。
- [中间件安全边界](中间件安全边界.md)：静态资源、短路、错误和敏感信息边界。

文档中的 API 以当前源码为准。`config/middleware.json` 的 `alias` 和 `priority` 会在应用装配阶段应用到框架的 `middleware.Pipeline`，但只能引用代码中已经注册的中间件别名，不能通过 JSON 反射构造 Go 中间件。

兼容代码继续使用 `PipeByName`（缺失别名静默忽略）；安全敏感的装配使用 `PipeByNameStrict`，缺失别名会返回 `ErrMiddlewareAliasNotFound`。代理协议和 Session Secure Cookie 只接受 HTTP 内核经过 `trusted_proxies` 校验后的结论，详见[中间件安全边界](中间件安全边界.md)。
