# 会话 ID、拒绝未知 ID 与认证轮换

会话 ID 是持有即可能获得会话数据的凭据。仅校验长度和字符不代表 ID 已由服务器分配；拒绝未知 ID 与认证成功后的身份轮换是两个不同控制。

## 默认拒绝未知 ID

在应用的 `config/session.json` 中设置：

```json
{
  "reject_unknown_id": true
}
```

该字段仅接受布尔值，缺省为 `true`。`session.DefaultConfig()` 也启用该策略。需要兼容采纳未知 ID 的应用可显式设置 `false`，严格浏览器档位会拒绝该值。配置按值复制，修改 `GetConfig()` 返回值只影响副本。

默认策略如下。

| 输入或后端结果 | 行为 |
| --- | --- |
| 合法外部 ID，当前配置的后端／前缀下无记录 | 生成新的随机 ID，不用传入 ID 建立记录。 |
| 合法、未过期且未撤销的已有记录 | 保留原 ID 与数据，包括启用本选项前保存的有效会话。 |
| 过期、已撤销或语法非法的 ID | 继续使用现有的新匿名会话处理。 |
| 后端读取失败或取消 | 返回原错误，保留客户端 Cookie。 |
| 记录损坏或超出大小限制 | 中断本次请求。客户端 Cookie 指向该记录时，按原作用域发送删除 Cookie。下一次请求可以重建会话。 |

Cookie 和显式 `NewRequestSessionWithIDAndSecure` 使用同一套 ID 采纳策略。生产 `browser_cookie` 应用配置非空 `session.var_session_id` 时会阻止启动，避免 POST 参数携带他人预先创建的会话。允许启用该参数的环境默认也会校验 ID 是否存在。若未知 POST ID 被拒绝，本次请求会使用服务端生成的新 ID。

不增加提前保存：无会话数据变更时，不会为拒绝的 ID 创建记录，也不会保存刚生成的匿名 ID；正常保存阶段清除无效客户端 Cookie。需要保存会话数据时，先原子持久化新 ID，成功后才发送对应 Cookie。已有 ID 撤销、冲突处理和响应提交边界保持不变。

## 登录与权限提升仍须轮换

应用应先完成凭据校验，再调用该请求 Session 的 `SetAuthenticated("identity", verifiedIdentity)`；该显式认证边界先校验并隔离业务值，再撤销旧 ID、生成新 ID，成功后才写入身份或权限值。它不会校验密码、注册身份，也不会从任意 `Set` 猜测认证完成。也可以继续正确地调用 `Regenerate()`，检查错误后再 `Set`。

`SetAuthenticated` 保留购物车等无关数据，使用与 `Set` 相同的键、JSON 值和容量限制。在驱动原子 `Update` 内合并后端最新数据、本请求的 Set/Delete/Clear 增量与认证值，按最终持久化格式实际编码完整信封，再检查 `MaxDataBytes`；通过后才撤销旧 ID。大小校验失败保持旧 ID、后端原记录、本地数据与增量及 Cookie 不变，旧 Cookie 仍可恢复验证码、CSRF 与原权限。替换已有身份先覆盖旧值后计算大小；未持久化的请求也先校验再改变 ID。

轮换与认证值发布按 `saveMu → mu` 持锁完成，`Save` 和响应提交不能夹入其中，同一请求的 Set/Delete/Clear 在认证转换结束后继续执行，不能在预检后插入新数据使校验失效。新状态采用预检实际编码的规范形式，后续 Set 的字节计数与保存格式一致，包括旧驱动数据中尚未转义的 HTML 字符。原始业务值仍在锁外只编码一次。已撤销 ID 的另一请求返回 `ErrSessionRevoked`。驱动在排他回调中检查有效期，因此等待存储锁期间已过期的记录不能被认证。

自定义驱动必须遵守 `Update` 的原子契约：回调返回错误时保留原记录，不提交返回值；不能在更新或回调中重入同一个请求 Session。预检通过不代表后续存储不会失败；底层提交后的错误或网络结果不确定，仍可能使旧 ID 已撤销，不能声称所有驱动错误都可回滚。任何轮换错误均不发布本地新身份，也不能报告登录成功。

helper 成功不表示已经持久化。最后仍须由 Session 中间件完成响应提交前保存，或显式检查 `Save()` 的错误。轮换或持久化失败不能向客户端报告登录成功。不要重用另一个请求的 Session 对象。可执行示例见 `session/session_authentication_example_test.go`。

本选项只检查记录是否存在且有效，不认证创建记录的代码来源。攻击者若已获得一份合法的匿名会话 ID，或会话被另一台宽松模式节点创建，它依然是“已有 ID”。因此，即便开启本选项，省略认证后的轮换仍可能造成会话固定。

框架已有 `Regenerate()` 会撤销持久化旧 ID，旧请求随后保存返回 `ErrSessionRevoked`，而不是重建原登录态。应用须正确处理该错误；轮换本身不是业务数据库事务，也不能撤销已经完成的外部副作用。

## 部署、混合模式与回滚

此功能不更改会话信封、Cookie 编码或 ID 格式。新旧模式可以读取相同的有效记录，但存在宽松节点时，“拒绝未知 ID”不构成整个集群的保证。所有接收请求的节点应采用一致策略；仍须在认证与权限变化处轮换，并隔离后端的读写权限。

旧二进制会严格拒绝未知配置字段。先升级代码，再启用新字段；回滚到不支持该字段的二进制前先移除字段，并明确回滚后不再具备未知 ID 拒绝能力。不要把记录校验和、配置开关或一次后端读取描述成跨节点身份来源证明。

`app.security_strict=true` 的 `browser_cookie` 档案要求实际 `session.reject_unknown_id` 为 true，详见[严格安全检查](strict-security.md)。所有节点应采用一致的 ID 采纳策略，并在认证成功及权限变化时通过 `SetAuthenticated` 或 `Regenerate` 轮换会话。

## 验证与依据

可执行示例见 `session/session_strict_id_example_test.go`。回归覆盖默认拒绝、明确选择兼容模式、生产启动检查、未知 ID 的 Cookie 和显式入口、惰性保存、有效与失效记录、持久化错误及并发请求隔离。中间件回归使用内存和文件后端，验证 POST、Cookie、认证轮换及旧请求重放。

设计参考 [OWASP Session Management Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html) 中的 “Session ID Generation and Verification: Permissive and Strict Session Management” 与 “Renew the Session ID After Any Privilege Level Change”。拒绝未知 ID 不能替代权限变化后的轮换。
