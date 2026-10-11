# 密码哈希、验证与资源预算

`password` 包提供基于 `golang.org/x/crypto/argon2.IDKey` 的 Argon2id 哈希，不实现登录存储、用户授权或会话管理，也不自动修改已有密码记录。框架缺少此 helper 不等于应用必然使用弱哈希。

## API 与默认参数

在应用启动时调用 `password.New(password.DefaultConfig())`，把返回的 `*password.Hasher` 复用于所有认证请求。`Hash(ctx, plaintext)` 返回可直接存储的 PHC 字符串；`Check(ctx, encoded, plaintext)` 返回匹配结果。便捷函数 `HashPassword`、`CheckPassword` 共享一个进程内的默认 Hasher。零值或 nil Hasher 被明确拒绝。

默认成本是 19 MiB 总内存、2 次迭代、1 个并行 lane，同时最多执行 2 项操作；盐为 `crypto/rand` 生成的 16 字节，派生键为 32 字节。格式示意：

```text
$argon2id$v=19$m=19456,t=2,p=1$<无填充标准Base64盐>$<无填充标准Base64键>
```

保存整个字符串。盐不是秘密，不应复用固定盐；不能只保存末尾的键，也不能把 Argon2id 换成 MD5、SHA1 或无盐快速哈希。`password/example_test.go` 是可编译执行的公开 API 示例。

| 设置 | 创建新哈希 | 验证已有哈希 |
| --- | --- | --- |
| 内存，KiB | 19456–65536 | 8 × lanes 至 65536 |
| 迭代次数 | 2–10 | 1–10 |
| 并行 lanes | 1–4 | 1–4 |
| 内存整除要求 | 必须是 4 × lanes 的整数倍 | 同左 |
| 单个 Hasher 的并发操作数 | 1–16，默认 2 | 与 Hash 共用预算 |

上述验证上限由库限定，不能由数据库中的字符串提高。先检查不超过 256 字节的编码、算法/版本、规范十进制参数、固定盐/键长度与规范 Base64，再进入 KDF。其他算法、Argon2 旧版本、不同盐/键长度和非规范编码不在本接口的接受范围内，应通过明确的迁移流程处理。

密码允许 1–1024 字节，包括 Unicode、空格、NUL 及任意字节；不做截断、去空格、大小写或 Unicode 归一化。长度按字节而不是字符计算。最低长度、泄漏密码检查等账户政策由应用负责。输入请求的总体积仍应在 HTTP 层限制，不应等完整请求被分配后才依靠本包长度校验。

## 登录失败、并发与取消

`Check` 的密码不匹配是 `(false, nil)`；编码错误、超限参数、空/超长密码、繁忙或取消则返回错误。可以通过 `errors.Is` 分类，错误内容不包含密码、完整哈希或随机设备细节。应用对外应统一无效凭据响应，不向客户端暴露账户是否存在、记录格式或内部失败原因。不存在的用户也应使用预生成的正常成本 dummy hash 完成验证，配合限流，避免直接返回形成枚举时序差异。

并发饱和立即返回 `ErrBusy`，不创建后台 goroutine、不排队、不自动重试。认证入口应有全局/账户级限流与负载策略；不要为每个请求创建新的 Hasher，也不要同时创建大量独立 Hasher 绕过预算。便捷函数的预算与自建 Hasher 不共享，分布式实例的预算也不共享。默认两项新哈希约需 38 MiB KDF 工作内存；验证允许的最大单项工作内存为 64 MiB，另有 Go/请求/GC 开销。这是活动计算预算，不是进程 RSS 硬上限。

传入请求 context。取消在计算前后检查；底层 Argon2 不能中途打断。计算中取消后仍须等待本次 KDF 完成，返回 context 错误并丢弃结果，之后才释放名额。不能把取消实现为提前释放名额但让后台哈希继续运行。代码尽量缩短临时字节副本寿命，但不承诺 Go 运行时中所有秘密副本均被可靠擦除。

## 参数升级与认证接入顺序

1. 从可信账户记录读取完整哈希，以请求 context 调用 `Check`。只有 `matched == true && err == nil` 才视为密码验证成功；不对错误执行盲目重试。
2. 验证成功后调用同一 Hasher 的 `NeedsRehash`。只有并行度相同、内存和迭代均不降低且至少一项提高时，才返回 true；更强、交叉取舍或不同并行度不会被自动降级。false 既不代表凭据有效，也不意味着所有可能的迁移都已完成。
3. 需要升级时，用仍在本次请求中的明文重新 `Hash`。以旧哈希作比较条件原子替换，防止覆盖并发密码修改。升级失败由业务决定延后，不自动重试，不把数据库写失败当成密码不匹配。
4. 完成其他账户/多因素检查后再创建登录状态，并轮换会话 ID。哈希 helper 不自动调用 Session、不生成 Cookie、不跳过多因素验证。

旧 bcrypt/PBKDF2 等记录应由独立且受成本限制的验证器处理，在成功登录后迁移到本格式，不得将旧摘要直接作为用户原密码重新哈希。不要把整个参数差异简单理解为“更弱”：并行度和内存/时间取舍需要单独评估。

## 验证和性能

回归覆盖独立 Argon2 实现生成的向量、随机盐、错误密码、字节边界、畸形/高成本编码、参数升级、熵源失败及共享并发预算；模糊测试只检查解析和准入属性，不因随机输入触发昂贵 KDF。执行：

```bash
go test -race -shuffle=on ./password
go test -run '^$' -fuzz '^FuzzPasswordEncodedHash$' -fuzztime=10s ./password
go test -run '^$' -bench '^BenchmarkPasswordHash$' -benchmem ./password
```

在部署机器上测量单次耗时、峰值并发、内存和失败负载，再提高 `Config` 成本。不要把某台开发机器的基准解释为所有生产环境的承诺；提高工作因子也不能替代账户保护或资源限额。本包随完整仓库 CI 运行，但不声称满足特定合规认证。

## 依据

- [Go 官方 Argon2 包](https://pkg.go.dev/golang.org/x/crypto/argon2)：使用 `IDKey` 实现 Argon2id，内存参数单位为 KiB。
- [OWASP Password Storage Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html)：19 MiB / 2 次迭代 / 1 lane 是交互式密码存储的最低推荐配置之一；成本应按实际负载测量。
- [RFC 9106](https://www.rfc-editor.org/rfc/rfc9106.html)：Argon2 参数和版本定义。该 RFC 的推荐配置与本库所选交互式默认成本不同，不把 OWASP 默认值声称为 RFC 推荐值。
