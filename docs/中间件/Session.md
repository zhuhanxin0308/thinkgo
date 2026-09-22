# Session

框架的 `middleware.Session` 把应用级 `*session.Session` 管理器转换为请求级会话中间件。它负责从请求 Cookie 初始化隔离的 Session，在下游完成后保存变更，并把保存产生的 Cookie 头提交到框架响应。

## 装配与请求数据

应用配置 `app.session_enable=true` 时，框架读取 `config/session.json`，创建 Session 管理器并自动加入全局管道：

```go
sessionManager := app.Session()
```

普通业务不需要手工创建中间件或修改管道。

中间件为每个请求调用 `NewRequestSession`，并以 `_session` 键写入 `context.Request`。控制器或服务应使用这个请求级对象，不要解析 Session 文件或复用另一个请求的 Session 实例。

## 保存顺序

处理过程严格是：

```text
读取 Cookie/后端记录
-> 写入 request.Set("_session", requestSession)
-> 调用下游
-> 绑定 ResponseAdapter
-> 保存会话
-> 提交 Set-Cookie 等响应头
-> 返回业务响应
```

`ResponseAdapter` 只暂存响应头，`Commit` 会通过 `context.Response.AddHeader` 的校验入口提交，避免直接修改响应头的防御性副本。会话保存失败时不会继续返回下游的成功响应。

## 状态映射

| 情况 | 响应状态 |
|---|---:|
| 管理器、请求、底层请求或 `next` 缺失 | 500 |
| Session Cookie 非法、重复或无法解析 | 400 |
| 后端初始化、绑定响应或 Cookie 提交失败 | 500 |
| Session 已撤销或保存期间持续并发修改 | 409 |

下游返回 `nil` 时，中间件保持 `nil`，不会为了写 Cookie 伪造成功响应，也不会进入保存和提交步骤。

## 配置边界

`config/session.json` 使用 ThinkPHP 默认字段 `name`、`var_session_id`、`type`、`store`、`expire` 和 `prefix`，并支持 `storage_path`、`redis`、`cookie_path`、`domain`、`secure`、`httponly`、`samesite`、`max_data_bytes` 等 Go 服务扩展。驱动支持 `file`、`cache`、`memory` 和 `redis`，默认配置为 `file`。Redis 驱动必须配置独立非空 `prefix`，使用原生 TTL 和按 Session ID 的分布式锁，适合多实例共享会话。Session Cookie 强制要求 `HttpOnly=true`；`SameSite` 必须是规范形式 `Lax`、`Strict` 或 `None`；未知字段、类型错误和越界数值会形成启动错误。

请求级 Session 的 `Set`、`Delete`、`Clear`、`Regenerate` 和 `Destroy` 都应处理返回的错误。键、JSON 值、单项大小和完整持久化信封大小都经过校验；并发保存按字段合并，遇到撤销或无法稳定快照时返回冲突。

## 文件驱动并发边界

文件 Session 使用同目录锁文件和 owner 身份复核保护读改写、删除和垃圾回收。Windows 锁句柄显式允许 `FILE_SHARE_DELETE`，因此回收旧文件不会因为另一个进程持有锁句柄而永久失败；删除发生前仍会重新确认 owner，锁已被替换时不会误删新锁。共享冲突和并发 DACL 设置只做有限次数的有界重试，达到上限后返回原始路径错误；等待截止瞬间确认锁文件已经消失时，驱动只执行一次最终非阻塞获取。该机制仍是同机文件驱动能力，不是分布式锁。

获取锁的竞争等待也有固定上限；高竞争下会继续退避让出 CPU，超过上限返回 `ErrSessionLockTimeout`。无竞争请求不会经过等待路径，因此不会增加正常请求延迟。

该锁只解决同机文件驱动的进程并发，不是 Redis、租约或任何分布式锁。多实例需要跨主机一致性时应使用 Redis 驱动，而不是把文件锁扩展为分布式协议。
