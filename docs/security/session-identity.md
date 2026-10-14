# 会话 ID、拒绝未知 ID 与认证轮换

会话 ID 是持有即可能获得会话数据的凭据。仅校验长度和字符不代表 ID 已由服务器分配；拒绝未知 ID 与认证成功后的身份轮换是两个不同控制。

## 显式拒绝未知 ID

在应用的 `config/session.json` 中设置：

```json
{
  "reject_unknown_id": true
}
```

该字段仅接受布尔值。缺省或 `false` 保留现有行为；本次不改变框架全局默认值。直接创建管理器时使用 `config := session.DefaultConfig()`，再设置 `config.RejectUnknownID = true`。配置按值复制，修改 `GetConfig()` 返回值不改变已创建管理器。

启用后的决策如下：

| 输入或后端结果 | 行为 |
| --- | --- |
| 合法外部 ID，当前配置的后端／前缀下无记录 | 生成新的随机 ID，不用传入 ID 建立记录。 |
| 合法、未过期且未撤销的已有记录 | 保留原 ID 与数据，包括启用本选项前保存的有效会话。 |
| 过期、已撤销或语法非法的 ID | 继续使用现有的新匿名会话处理。 |
| 后端读取失败、取消或记录损坏 | 保留原错误，不能将失败当成“不存在”而继续业务。 |

Cookie 和显式 `NewRequestSessionWithIDAndSecure` 入口使用相同判断。`var_session_id` 仍默认关闭；业务确有必要启用时，POST 来源也经过此判断，其既有优先级不变，不因拒绝未知 POST ID 而回退到另一份 Cookie 身份。

不增加提前保存：无会话数据变更时，不会为拒绝的 ID 创建记录，也不会保存刚生成的匿名 ID；正常保存阶段清除无效客户端 Cookie。需要保存会话数据时，先原子持久化新 ID，成功后才发送对应 Cookie。已有 ID 撤销、冲突处理和响应提交边界保持不变。

## 登录与权限提升仍须轮换

应用应先完成凭据校验，再调用该请求 Session 的 `SetAuthenticated("identity", verifiedIdentity)`；该显式认证边界先校验并隔离业务值，再撤销旧 ID、生成新 ID，成功后才写入身份或权限值。它不会校验密码、注册身份，也不会从任意 `Set` 猜测认证完成。也可以继续正确地调用 `Regenerate()`，检查错误后再 `Set`。

`SetAuthenticated` 保留购物车等无关数据，使用与 `Set` 相同的键、JSON 值和容量限制。输入错误在轮换前返回；撤销失败不发布传入身份值。轮换与身份写入共用保存锁，`Save` 和响应提交不能夹入其中。已有认证状态的权限提升失败会保持原权限，不发布新权限；已撤销 ID 的另一请求返回 `ErrSessionRevoked`。合并后完整数据超限可能在旧 ID 撤销后使写入失败，仍须向客户端返回失败。

helper 成功不表示已经持久化。最后仍须由 Session 中间件完成响应提交前保存，或显式检查 `Save()` 的错误。轮换或持久化失败不能向客户端报告登录成功。不要重用另一个请求的 Session 对象。可执行示例见 `session/session_authentication_example_test.go`。

本选项只检查记录是否存在且有效，不认证创建记录的代码来源。攻击者若已获得一份合法的匿名会话 ID，或会话被另一台宽松模式节点创建，它依然是“已有 ID”。因此，即便开启本选项，省略认证后的轮换仍可能造成会话固定。

框架已有 `Regenerate()` 会撤销持久化旧 ID，旧请求随后保存返回 `ErrSessionRevoked`，而不是重建原登录态。应用须正确处理该错误；轮换本身不是业务数据库事务，也不能撤销已经完成的外部副作用。

## 部署、混合模式与回滚

此功能不更改会话信封、Cookie 编码或 ID 格式。新旧模式可以读取相同的有效记录，但存在宽松节点时，“拒绝未知 ID”不构成整个集群的保证。所有接收请求的节点应采用一致策略；仍须在认证与权限变化处轮换，并隔离后端的读写权限。

旧二进制会严格拒绝未知配置字段。先升级代码，再启用新字段；回滚到不支持该字段的二进制前先移除字段，并明确回滚后不再具备未知 ID 拒绝能力。不要把记录校验和、配置开关或一次后端读取描述成跨节点身份来源证明。

`app.security_strict=true` 的 `browser_cookie` 档案要求显式 `session.reject_unknown_id=true`，详见[严格安全检查](strict-security.md)。直接 Session 包调用仍保留兼容默认；所有节点都应采用一致 ID 策略并接入认证轮换 helper。`Secure`、`HttpOnly`、`SameSite`、CSRF、可信代理、会话有效期、登录限流及账户授权仍需独立配置。该选项不增加登录路由、不自动轮换密码、不改变 Session 的存储容量，也不为应用创建完整认证系统。

## 验证与依据

可执行示例见 `session/session_strict_id_example_test.go`。回归包含默认兼容、严格布尔配置、未知 ID 的 Cookie／显式入口、惰性保存、有效与失效记录、存储错误、并发请求隔离，以及另一节点在首次读取后建立记录的场景。中间件回归使用实际内存和文件后端，检查 POST／Cookie 输入、持久化 Cookie、认证轮换、旧请求重放与轮换失败；这不等于远程 Redis、多主机或真实浏览器验证。

设计参考 [OWASP Session Management Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html) 中的 “Session ID Generation and Verification: Permissive and Strict Session Management” 与 “Renew the Session ID After Any Privilege Level Change”。拒绝未知 ID 不能替代权限变化后的轮换。
