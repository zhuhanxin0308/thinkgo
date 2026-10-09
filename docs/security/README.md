# 安全使用边界

以下指南区分框架保证与应用职责。兼容 API 不会自动替业务完成对象级授权、上传内容校验或部署隔离。

| 主题 | 指南 |
| --- | --- |
| 数据库错误对外展示 | [内部错误与公共响应](database-errors.md) |
| 参数来源与授权对象 | [Request.Route / Param 的安全用法](request-parameter-sources.md) |
| 内部路径与对外文件引用 | [Local.Path、上传 key 和 URL](filesystem-paths.md) |
| 来自用户输入的跳转 | [站内重定向](local-redirects.md) |
| 锁竞争负载 | [缓存锁退避](cache-lock-contention.md) |
| 开发服务器监听 | [回环默认值与显式外部监听](development-listen-address.md) |
| 事件作用域与安全检查 | [HttpRun 的项目级边界](event-listener-scope.md) |
| CLI 清理范围 | [项目目录清理边界](clear-project-boundary.md) |

指南中的可执行示例和回归代码随正常包测试运行；完整发布验收仍由仓库的全量、竞态、多平台、真实服务、覆盖率与安全扫描门禁执行。
