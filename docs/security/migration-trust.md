# 迁移代码、批准清单与首次执行

迁移文件是可执行代码，必须审查来源、Up 与 Down 的全部操作。`NewSQL` 的 SHA-256 校验和用于内容标识和已应用迁移漂移检查，不是作者签名，不判断 SQL 的业务安全性，也不能单独认证首次收到的迁移。

## 可选的批准准入

`NewSQLWithApproval(name, upStatements, downStatements, approvals)` 在构造迁移时，将名称及双向语句校验和与独立审查的 `map[string]string` 核对。没有该名称、校验和为空或不相符时，返回 `nil` 迁移及可通过 `errors.Is(err, migration.ErrMigrationNotApproved)` 判断的错误。非法迁移定义继续返回 `ErrInvalidMigration`，与已应用后的 `ErrMigrationDrift` 区分。

这个接口不连接数据库，也不读取迁移历史，因此新数据库、首次应用时也能拒绝未批准定义。必须处理错误并在创建或运行 Runner 前构造整个迁移计划；不能忽略拒绝项后执行其余部分，也不能逐项批准后立即执行以伪装整批准入。它不是 Runner 的全局策略，更不会自动改写既有注册代码。

以下批准值对应已审查的固定 SQL 示例，不是在接收到 SQL 后自动生成的：

```go
package schema

import "github.com/zhuhanxin0308/thinkgo/v3/migration"

func reviewedMigration() (migration.Migration, error) {
    approvals := map[string]string{
        "202610120001_reviewed_table": "048a4555502260d95723a632a30b9d90efa2fd8f63dfa60b0244825e3299c03e",
    }
    return migration.NewSQLWithApproval(
        "202610120001_reviewed_table",
        []string{
            "CREATE TABLE approved_items (id INTEGER PRIMARY KEY)",
            "INSERT INTO approved_items (id) VALUES (51)",
        },
        []string{"DROP TABLE approved_items"},
        approvals,
    )
}
```

审核方可在受控的离线审批流程中，通过 `NewSQL(...).Checksum()` 取得待固定的值，但必须先审查全部语句及影响，并保护批准清单的变更权限。禁止在应用启动时从刚下载或未审查的 SQL 自动计算批准值；这样只是在让输入批准自己。若攻击者能同时改写 SQL 和清单，或修改调用代码以绕过新接口，该检查不提供保护。

## 精确保证与生命周期

校验和沿用 `NewSQL` 的现有编码：每条语句先去除首尾空白，再按顺序覆盖 Up、Down 两个方向及语句分隔。不是文件字节级签名；首尾空白变化不改变结果，语句顺序、方向或实质内容变化会改变结果。框架不按分号猜测拆分 SQL，不增加关键词黑名单，也不把包含 `DROP` 一概认定为未批准操作。合法回滚经审查后可以包含删除语句。

SQL 切片在构造时由已有 `NewSQL` 复制；调用方随后替换原切片元素不会改变已批准定义。清单只在构造时读取、不由迁移保留；随后修改清单不会撤销已经返回的迁移对象。构造期间不得并发写清单。需要调整审批策略时应先停止本次执行计划，重新构造并验证全部定义，而不是热修改清单期望已创建对象自动失效。

现有 `New`、`NewSQL`、注册表与 Runner 的语义保持不变。新接口只为主动接入的 SQL 迁移提供构造期准入；程序化迁移和 Go 包的初始化逻辑仍是可信代码，不受此校验沙箱化。直接调用 `Up` 的事务、锁与历史保证也不会因批准而增加；使用既有 Runner 和数据库存储完成正常迁移协议。

## 部署职责

审批至少涵盖目标数据库、账户权限、对象范围、预计锁持有时间、数据损失、备份及恢复演练。批准 SQL 文本不证明当前环境适合执行：相同的语句可能在不同数据库、schema、角色、触发器或会话设置下产生不同效果。批准清单不包含这些运行环境信息，部署必须独立验证。

迁移账户应采用所需的最小权限；生产发布须审查来源仓库、锁定依赖和构建来源。若需要作者或构建者认证，应使用真实签名、可信密钥／构建身份与可验证的来源证据。本功能没有实现签名验证，不能宣称达到某个 SLSA 等级。有关来源证据和预期值的独立核对，参见 [SLSA v1.2：Verifying artifacts](https://slsa.dev/spec/v1.2/verifying-artifacts)。

## 验证范围

回归覆盖缺失和错配批准、名称绑定、双向 SQL、不可逆迁移、输入快照、上下文／后端错误、并发只读构造和固定审查向量。启用 CGO 的测试使用真实 SQLite，检查首次拒绝没有创建迁移历史或业务表、受保护记录保留，以及批准后的正常应用、历史与回滚。它不是远程数据库、恶意 Go 依赖或完整供应链攻击验证。可执行示例位于 `migration/sql_approval_example_test.go`。
