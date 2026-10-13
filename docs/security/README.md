# 安全使用边界

以下指南区分框架保证与应用职责。兼容 API 不会自动替业务完成对象级授权、上传内容校验或部署隔离。

| 主题 | 指南 |
| --- | --- |
| 原生 SQL 与绑定参数 | [固定结构、参数化与查询快照](database-raw-sql.md) |
| 内存缓存容量与过期 | [过期回收、FIFO 与资源预算边界](memory-cache-capacity.md) |
| 预发与显式安全检查 | [不改变环境名的生产级配置检查](strict-security.md) |
| 不可信文件上传 | [显式类型配对、大小上限与原子发布](upload-policy.md) |
| 通用与认证 Cookie | [显式属性、工厂隔离与认证职责](cookie-policies.md) |
| 迁移代码与独立批准 | [首次执行准入与可信来源边界](migration-trust.md) |
| SQL 连接数预算 | [显式连接池上限与部署容量](database-pool-budget.md) |
| 会话 ID 与认证轮换 | [拒绝未知 ID、登录与混合模式边界](session-identity.md) |
| 生产限流配置 | [HTTP 启动提醒与部署职责](rate-limit-startup.md) |
| 密码存储与计算预算 | [Argon2id 哈希、验证与升级](password-hashing.md) |
| 字面搜索与通配模式 | [WhereLikeLiteral 与授权范围](database-literal-like.md) |
| 插入完成但主键读取失败 | [部分写入与事务结果](database-insert-results.md) |
| 数据库错误对外展示 | [内部错误与公共响应](database-errors.md) |
| 参数来源与授权对象 | [Request.Route / Param 的安全用法](request-parameter-sources.md) |
| 内部路径与对外文件引用 | [Local.Path、上传 key 和 URL](filesystem-paths.md) |
| 来自用户输入的跳转 | [站内重定向](local-redirects.md) |
| 锁竞争负载 | [缓存锁退避](cache-lock-contention.md) |
| 开发服务器监听 | [回环默认值与显式外部监听](development-listen-address.md) |
| 事件作用域与安全检查 | [HttpRun 的项目级边界](event-listener-scope.md) |
| CLI 清理范围 | [项目目录清理边界](clear-project-boundary.md) |

指南中的可执行示例和回归代码随正常包测试运行；完整发布验收仍由仓库的全量、竞态、多平台、真实服务、覆盖率与安全扫描门禁执行。
