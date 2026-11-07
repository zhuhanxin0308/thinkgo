# 2026-11-07 通用业务框架实测报告

## 实测完成情况

本次 **8 个框架、9 种运行配置、5 个业务场景、2 种负载模型、各 3 次重复，共 270 轮**正式测量已全部完成。全部轮次均保留并通过业务审计，运行程序于 2026-11-07 22:54:23 UTC 正常退出。[完整轮次索引](raw/index.json)、[进度记录](raw/progress.jsonl)。

| 请求计数 | 闭环 135 轮 | 开放 135 轮 | 合计 |
|---|---:|---:|---:|
| 计划请求 | 29,786,284 | 2,730,510 | 32,516,794 |
| 发出并成功完成 | 29,786,284 | 2,730,506 | 32,516,790 |
| 发送窗口内成功完成 | 29,777,674 | 2,730,343 | 32,508,017 |
| 排空期间成功完成 | 8,610 | 163 | 8,773 |
| 调度丢弃 | 0 | 4 | 4 |
| HTTP 状态／传输／语义错误 | 0／0／0 | 0／0／0 | 0／0／0 |

4 次丢弃全部来自有效轮 `open-order-beego-1`，原因是调度时已超过发送窗口；该轮所有已发请求均成功，结果未被删除或重测替换。270 轮数据库审计均通过，未确认订单及评论均为 0。正式运行中还发生过一次预热阶段中断和会话恢复，具体经过与环境状态边界见后文。数据是确定性合成业务数据；实际执行了本地 HTTP、数据库事务与 Redis 操作，不能据此推导生产流量的成功率保证。

## 闭环实测：吞吐与三轮范围

固定 64 并发的闭环阶段完成了 `9 配置 × 5 场景 × 3 次重复 = 135 轮`。全部轮次的状态、传输、语义错误及丢弃均为零，数据库审计全部通过，未发现未确认提交。闭环共成功完成 **29,786,284** 次请求，其中 **29,777,674** 次在发送窗口内完成，另有 **8,610** 次在排空期间完成；主吞吐只使用窗口内成功数。证据为 [闭环轮次目录](raw/rounds/) 中各 `closed-*/round.json` 与同轮 `audit.json`。

下表单位为**成功请求/秒**，每格为**三轮中位数 [最小值–最大值]**。所有数值显示一位小数，实际计算保留原始精度。范围来自实际重复，没有删除合法的慢轮，也不是置信区间。

| 配置 | 商品目录 | 商品详情 | 事务下单 | 工单评论 | 混合业务 |
|---|---:|---:|---:|---:|---:|
| ThinkGo | 11,483.5 [11,452.8–11,580.5] | 15,254.9 [15,122.2–15,337.5] | 2,575.4 [2,396.8–2,615.9] | 5,839.1 [4,310.7–5,868.2] | 6,222.6 [6,073.3–6,732.6] |
| Gin | 14,389.3 [12,972.0–14,417.5] | 23,149.7 [20,430.2–23,209.7] | 2,526.4 [2,384.5–2,640.9] | 6,382.3 [6,306.1–6,467.2] | 6,679.1 [5,503.3–7,330.0] |
| Echo | 14,350.8 [13,919.8–14,569.1] | 23,291.3 [16,495.1–23,555.0] | 2,634.4 [2,620.0–2,650.4] | 6,458.4 [3,447.5–6,533.9] | 6,764.0 [6,753.8–7,202.8] |
| Fiber | 15,044.7 [14,241.4–15,176.6] | 25,843.4 [11,586.3–26,237.5] | 2,573.1 [2,479.3–2,584.7] | 5,395.7 [5,004.7–6,731.6] | 7,580.1 [6,336.8–7,587.2] |
| Beego | 13,806.4 [13,721.8–13,872.2] | 21,117.7 [20,543.7–21,222.4] | 2,219.6 [2,160.8–2,663.7] | 6,416.4 [6,015.2–6,543.1] | 6,920.8 [6,739.6–7,115.1] |
| Iris | 14,487.4 [13,289.1–14,500.5] | 23,468.9 [23,276.0–23,480.0] | 2,561.9 [2,418.0–2,645.6] | 6,527.6 [5,659.2–6,603.6] | 7,104.6 [6,629.3–7,567.1] |
| ThinkPHP-FPM | 4,462.6 [4,299.1–4,503.2] | 4,521.7 [4,075.8–4,540.7] | 2,139.2 [1,097.8–2,244.2] | 3,382.4 [2,952.2–3,612.8] | 3,846.3 [3,837.6–3,882.4] |
| Laravel-FPM | 1,443.6 [1,442.8–1,469.4] | 1,491.4 [1,386.9–1,498.1] | 1,142.0 [947.6–1,142.7] | 1,308.3 [1,302.0–1,317.9] | 1,361.6 [1,353.4–1,372.0] |
| Laravel-Octane | 1,867.1 [1,767.3–1,870.7] | 1,929.6 [1,918.2–2,004.6] | 1,282.8 [1,261.2–1,289.6] | 1,616.0 [1,408.0–1,630.1] | 1,596.2 [1,500.3–1,669.1] |

### ThinkGo 的具体表现

**目录与详情是本次观察中较明确的吞吐差距。** ThinkGo 的目录中位数比其他五个 Go 配置低 16.82%–23.67%；其最高一轮 11,580.5 RPS 仍低于其他 Go 配置各自的最低值。详情中位数低 27.76%–40.97%，与 Gin、Echo、Beego、Iris 的三轮范围不重叠；但 Fiber 详情第一轮只有 11,586.3 RPS，因此不能说 ThinkGo 在详情的每一次观察都最慢。[ThinkGo 目录第三轮](raw/rounds/closed-catalog-thinkgo-3/round.json)、[ThinkGo 详情第三轮](raw/rounds/closed-detail-thinkgo-3/round.json)、[Fiber 详情第一轮](raw/rounds/closed-detail-fiber-1/round.json)。

**订单吞吐属于接近的一组。** ThinkGo 的中位数数值上位于六个 Go 配置第二，比 Fiber 高 0.09%、Iris 高 0.53%、Gin 高 1.94%，同时比 Echo 低 2.24%。除 Beego 外，五个 Go 配置中位数集中在 2,526.4–2,634.4 RPS；ThinkGo 与 Fiber、Iris、Gin 的观察范围重叠，Echo 的最低值则比 ThinkGo 最高值高约 4.1 RPS。三次观察及这一窄幅间隔不支持把小数位上的次序解释为稳定领先或统计显著性。相对 Beego 的中位数高 16.03%，但其最快一轮也高于 ThinkGo 的最高值，仍应保留波动边界。

**混合业务中位数位于本次 Go 组末位。** ThinkGo 为 6,222.6 RPS，比其他五个 Go 配置低 6.83%–17.91%，但与 Gin、Fiber、Iris 的范围重叠。Echo、Beego 的最低值仅比 ThinkGo 最高值分别高约 21.2、7.0 RPS，这种很小的观察间隙也不能视为统计显著性。工单评论中位数为 5,839.1 RPS，数值上位于 Go 组第五，第一轮较慢，需要结合其完整范围阅读。[ThinkGo 工单第一轮](raw/rounds/closed-ticket-thinkgo-1/round.json)、[ThinkGo 混合第二轮](raw/rounds/closed-mixed-thinkgo-2/round.json)。

上述数据说明了特定请求路径上的表现，尚未提供成本归因；没有进行对应 CPU profile、分配 profile 或等待分析，不能据此断言差距由路由、中间件、反射、GC、数据库或宿主调度中的某一项造成。后续若优化 ThinkGo，应先对目录和详情的真实路径做有针对性的诊断，再保持合同、SQL、数据与运行条件一致进行前后比较。

### 波动与排名的边界

用 `(最大值 − 最小值) / 中位数` 描述三轮相对极差，45 个配置与场景组中有 **20 组至少为 10%**，其中 **8 组至少为 20%**。该指标不是变异系数或置信区间。较大的观察波动包括：

| 配置与场景 | 第一轮 RPS | 第二轮 RPS | 第三轮 RPS | 相对极差 |
|---|---:|---:|---:|---:|
| Fiber 详情 | 11,586.3 | 25,843.4 | 26,237.5 | 56.69% |
| ThinkPHP-FPM 下单 | 2,139.2 | 1,097.8 | 2,244.2 | 53.59% |
| Echo 工单 | 6,533.9 | 6,458.4 | 3,447.5 | 47.79% |
| Fiber 工单 | 5,395.7 | 6,731.6 | 5,004.7 | 32.01% |
| Echo 详情 | 16,495.1 | 23,555.0 | 23,291.3 | 30.31% |

这些较慢轮次仍然业务成功、审计通过，没有证据将其剔除为无效异常值。[ThinkPHP 下单第二轮](raw/rounds/closed-order-thinkphp-fpm-2/round.json)、[Echo 工单第三轮](raw/rounds/closed-ticket-echo-3/round.json)。Fiber 的目录、详情、混合中位数最高，但三轮范围仍与若干配置重叠，不能称为稳定冠军。不同业务的工作量不同，本文不把五项 RPS 简单平均为综合分数；需要固定权重的组合时，使用直接测量的混合业务结果。

本次 Laravel-Octane 的五项吞吐中位数比 Laravel-FPM 高 **12.33%–29.38%**，且这五对观察范围没有重叠。这是本次 Octane/RoadRunner 与 FPM 配置的实测关系，不是对所有 PHP 部署的普遍比例；吞吐关系也不能直接代替同到达率下的尾延迟关系。

### 由完整闭环结果确定的共同开放率

五个业务的最小闭环中位数均来自本次 Laravel-FPM 配置。按协议取其一半并向下取整，结果与 [arrival-rates.json](raw/arrival-rates.json) 一致：

| 业务 | 最小闭环中位数 RPS | 共同开放率（请求/秒） | 每轮 30 秒计划请求数 |
|---|---:|---:|---:|
| 商品目录 | 1,443.6000 | 721 | 21,630 |
| 商品详情 | 1,491.4333 | 745 | 22,350 |
| 事务下单 | 1,142.0000 | 571 | 17,130 |
| 工单评论 | 1,308.3333 | 654 | 19,620 |
| 混合业务 | 1,361.5667 | 680 | 20,400 |

这些共同到达率约相当于 ThinkGo 对应闭环中位数的 4.88%–22.17%，该比例不是 CPU 利用率。开放阶段比较的是这组共同流量下的延迟与资源消耗，不代表各 Go 配置临近饱和时的尾延迟，也没有覆盖完整容量曲线。

## 比较范围与证据原则

本次实验比较 **8 个框架、9 种运行配置**：ThinkGo、Gin、Echo、Fiber、Beego、Iris、ThinkPHP，以及分别运行于 PHP-FPM 和 Octane / RoadRunner 的 Laravel。实验使用本地真实 HTTP 请求、PostgreSQL 事务和 Redis 缓存，覆盖商品目录、商品详情、创建订单、工单评论与混合请求五个场景。正式矩阵定义为 `9 配置 × 5 场景 × 2 负载模型 × 3 次重复 = 270 轮`。

比较对象是这些**具体部署配置执行共同业务合同的表现**。六个 Go 框架复用同一 Go 业务层，PHP 配置复用同一 PHP 业务层；两种语言使用相同的 SQL 文本、事务顺序、缓存算法、数据规模与响应字段。各框架保留原生路由、中间件、请求绑定、JSON 响应和应用生命周期。ThinkGo 使用独立应用配置与原生 `App.Run`，模块中的本地 `replace` 明确指向当前仓库；业务适配没有改动各框架本体源码。[源码：Go 业务层](../../../benchmarks/business/go/internal/business/)、[框架适配](../../../benchmarks/business/go/internal/adapters/)、[PHP 业务层](../../../benchmarks/business/php/shared/src/)、[模块锁定](../../../benchmarks/business/go/go.mod)。

这套设计减少 SQL、查询数量和业务算法不同带来的混淆，但不会隔离出“纯路由器成本”：语言运行时、数据库驱动、JSON 实现、框架请求生命周期、连接管理与 Nginx 上游协议仍在被测路径内。结果也不覆盖各框架 ORM、模板引擎、队列、完整权限体系及其生态。FPM 与常驻 PHP 单独列示，不能把某一种 PHP 部署的结果推广为 PHP 的唯一表现。

实验身份以 [raw/manifest.json](raw/manifest.json) 为准。清单保存正式运行时的本地 Git 标识、`benchmarks/business` 已跟踪文件摘要、运行二进制摘要、PHP 配置摘要、协议与主机可见信息。本文使用仓库相对源码链接；清单中的本地 Git 标识仅用于追溯本次冻结状态，不把它解释为已经存在的远端可点击提交。复核时应同时核对源码、锁文件、配置及二进制摘要，不能仅凭框架版本号认定两个实验相同。

冻结源码已发布于分支 `bench/real-business-20261107`，对应远端提交 [6f332ab0122831ed75044aa63910aa2b57947b34](https://github.com/zhuhanxin0308/thinkgo/commit/6f332ab0122831ed75044aa63910aa2b57947b34)。它与本地正式测量提交 `db21dda7ae1761bf68c5848884a5ba2ed411088c` 的源码 tree 均为 `6a4361fa3103d54be47971978a9f8517199320d7`。发布通道未保留原提交作者和时间等元数据，因此提交 SHA 不同；原始 36 次检查点保存在 [source-checkpoints.bundle](validation/source-checkpoints.bundle)，归档摘要与说明见 [验证清单](validation/test-summary.json)。相同源码 tree 不等于相同 Git 提交，带有 VCS 信息的 Go 二进制也可能因此具有不同摘要。

## 版本与运行条件

### 框架与执行模式

下表为本次冻结并实际接入的版本，完整身份见 [实验协议](raw/contracts/protocol.json)、[Go 模块锁](../../../benchmarks/business/go/go.mod)、[ThinkPHP Composer 锁](../../../benchmarks/business/php/thinkphp/composer.lock) 和 [Laravel Composer 锁](../../../benchmarks/business/php/laravel/composer.lock)。它们是本次实验的版本快照，不构成对未来“最新版”的持续声明。

| 配置标识 | 框架版本 | 执行模式 |
|---|---|---|
| `thinkgo` | ThinkGo 3.0.3 | Go，原生应用生命周期，`net/http` |
| `gin` | Gin 1.12.0 | Go，`net/http` |
| `echo` | Echo 5.4.4 | Go，`net/http` |
| `fiber` | Fiber 3.6.1 | Go，`fasthttp` |
| `beego` | Beego 2.3.10 | Go，`net/http` |
| `iris` | Iris 12.2.11 | Go，`net/http` |
| `thinkphp-fpm` | ThinkPHP 8.1.4 | PHP-FPM + OPcache |
| `laravel-fpm` | Laravel 13.40.0 | PHP-FPM + OPcache |
| `laravel-octane` | Laravel 13.40.0 | Octane 2.23.0 / RoadRunner 3.0.1 |

运行依赖固定为 Go 1.27.3、PHP 8.5.12、PostgreSQL 18.6、Redis 8.10.3、Nginx 1.30.6、phpredis 6.3.0、Composer 2.11.1。负载直方图库为 HdrHistogram Go 1.4.0；Go 数据驱动为 lib/pq 1.12.3 与 go-redis 9.23.0。控制端使用 Python 3.12.14，资源采样使用 psutil 7.2.2。固定下载地址、归档 SHA256 与安装参数见 [runtime-sources.json](../../../benchmarks/business/contracts/runtime-sources.json)，Python wheel 版本与摘要见 [requirements.txt](../../../benchmarks/business/tools/requirements.txt)。

Swoole、Workerman 的候选运行方式受本环境系统调用限制，没有进入正式性能矩阵。Laravel 常驻模式使用在此环境中实际运行并通过 HTTP 验收的 RoadRunner；未运行的配置没有性能分数，也不以启动失败推断其性能。

### 主机可见信息与 CPU 亲和性

[运行清单](raw/manifest.json) 记录：Linux 6.18.57、x86_64、CPU 型号字符串 `AMD EPYC 9V74 80-Core Processor`，当前进程可见 CPU 编号为 0–8，`MemTotal` 为 10,206,472 kB。型号中的 “80-Core” 是宿主型号描述，实验没有获得 80 个可用核。cgroup CPU 配额不可读取，清单明确记录 `exclusive_cpu_allocation=false`；这些数字不能替代容器或宿主的实际资源配额。

| 进程组 | 允许运行的 CPU | 计量与说明 |
|---|---|---|
| 应用及 Nginx | 0、1、2 | 合并为 `application` 组；Go 应用 `GOMAXPROCS=3` |
| PostgreSQL 及后代 | 3、4、5 | 独立记录数据库资源 |
| 负载发生器 | 6、7 | `GOMAXPROCS=2`，同时记录进程 CPU |
| Redis | 8 | 独立记录缓存资源 |
| Python 控制与采样 | 8 | 与 Redis 共享此 CPU；记录控制进程 CPU |

上述配置由 `taskset` 和进程亲和性实施，表示允许调度到哪些 CPU，**不是独占 CPU 或硬配额**。其他宿主活动、共享内存带宽、缓存与调度仍可能造成波动。实验分别记录应用、数据库、Redis、负载端及控制端，避免把整个环境的 CPU 代价都归给框架。[启动实现](../../../benchmarks/business/tools/native.py)、[调用与采样实现](../../../benchmarks/business/tools/run.py)。

### 网关、连接与 PHP 预处理

全部请求经过同一 Nginx 网关，负载端使用 loopback HTTP/1.1 keep-alive；不启用 TLS、HTTP/2、gzip 或网关访问日志。Nginx 使用 1 个 worker、`worker_connections=4096`，客户端连接 `keepalive_timeout=60`、`keepalive_requests=1000000`。请求与上游读写超时为 5 秒，JSON 正文上限为 16,384 字节。正式测量包含本地 TCP 和网关成本，不包含公网 RTT、TLS 握手或跨机数据库网络。[网关配置生成](../../../benchmarks/business/tools/native.py)。

Go 与 Octane 通过 HTTP 上游代理，显式配置 `keepalive 64`、HTTP/1.1 及清空 `Connection` 头。FPM 通过直接 `fastcgi_pass` 和 `fastcgi_keep_conn on`，未显式配置与前者相同的命名 upstream 空闲连接池。两者上游传输与连接策略存在差异，不能描述为相同的 64 连接池，也不能仅凭配置断言每次请求的实际连接复用情况。Nginx 官方文档把 `keepalive` 定义为空闲连接缓存上限，而非上游总连接数上限。[Nginx upstream 官方文档](https://nginx.org/en/docs/http/ngx_http_upstream_module.html#keepalive)。

数据库连接预算统一为 24：Go 的 `database/sql` 设置最大打开及空闲连接数均为 24；FPM 采用静态 24 worker；Octane 采用 24 worker。PHP 每 worker 复用一个持久 PDO 连接与一个持久 Redis 连接。Go Redis 设置的是基础池参数 `PoolSize=24`，没有单独设置 `MaxActiveConns`，不能把它表述为 Redis 严格 24 连接硬上限；本实验没有逐连接观测，亦不据此推断实际连接数超过 24。Go 的并发请求仍可在连接池前排队，PHP 的进程请求调度具有自身语义，统一数据库连接预算没有抹平这类差异。PDO 使用参数化执行、关闭模拟预处理和服务器命名预处理，避免为 PHP 单独加入长期命名语句缓存优势。[Go 连接配置](../../../benchmarks/business/go/internal/business/service.go)、[PDO 实现](../../../benchmarks/business/php/shared/src/PdoDatabase.php)、[Redis 实现](../../../benchmarks/business/php/shared/src/RedisCache.php)。

PHP 应用以生产配置运行，关闭调试；启动前构建框架配置与路由缓存。FPM 开启 OPcache、关闭时间戳验证及 JIT；公共 PHP 配置开启 CLI OPcache，JIT 缓冲区为 0。Octane 按其常驻生命周期保留无请求状态的业务依赖。PCOV 只用于测试，不加载到生产测量配置。FPM 请求内的静态对象缓存不等同于 Octane 的进程内跨请求复用，底层持久连接另按驱动机制保留。[PHP 启动配置](../../../benchmarks/business/tools/native.py)、[运行依赖配置](../../../benchmarks/business/tools/bootstrap.py)、[依赖工厂](../../../benchmarks/business/php/shared/src/Factory.php)。

FPM 和 Octane 均在服务启动前，把共同的 `dataset.json` 与 `queries.json` 编译为可由 OPcache 缓存的 PHP 数组；测量路径不反复读取和解析这两个 JSON 文件。快照以内容摘要命名，启动时核对来源与文件摘要，每个 PHP 轮次归档实际使用的原始 `contract-snapshot.php`，并由本轮证据清单绑定。Go 在应用启动时读取同一合同。这个预处理属于应用准备阶段，没有替换业务 SQL 或响应合同。[快照编译器](../../../benchmarks/business/php/shared/compile-contract.php)、[快照校验](../../../benchmarks/business/php/shared/src/ContractSnapshot.php)、[轮次归档](../../../benchmarks/business/tools/run.py)。

## 共同业务合同与数据集

### 数据规模、身份与输出字段

全部数据均为确定性合成数据，种子为 `20261107`。初始数据包括 4 个租户、4,000 个用户、128 个分类、100,000 个商品、300,000 条商品规格、20,000 个工单和 60,000 条初始评论；订单、订单明细与订单审计从空表开始。每租户有 25,000 个商品，其中本地编号为 10 的倍数者下架，合计 90,000 个活动商品。每商品初始库存为 1,000,000，每工单初始评论数与版本均为 3。[数据合同](raw/contracts/dataset.json)、[种子 SQL](raw/contracts/seed.sql)、[实际种子计数](raw/seed-counts.json)。

用户、商品与工单的全局主键按照 `(tenant - 1) × 每租户数量 + 本地编号` 生成。认证令牌为 `Bearer tenant.user.expiry.signature`，前三段为规范十进制文本，签名为 HMAC-SHA256 十六进制值；服务端验证签名、有效期及用户与租户的编号归属，业务查询再限制租户。负载端在计时前生成并缓存令牌。这里测试的是明确限定的签名身份与业务归属验证，没有引入登录、密码散列、OAuth 服务或用户数据库查询。[认证实现](../../../benchmarks/business/go/internal/business/auth.go)、[请求轨迹](../../../benchmarks/business/go/internal/loadrun/workload.go)。

响应仅返回调用方使用的字段。商品卡片为 `id/name/price_cents/category/stock`；详情另外提供 `description/specs`，其中规格项只有 `name/value`；订单返回 `order_id/total_cents/status/items`；评论返回 `comment_id/ticket_id/version/status`。金额统一为整数分。数据表中的内部邮箱、内部成本等字段不进入响应，负载发生器逐请求检查字段白名单、类型与业务关系。[响应类型](../../../benchmarks/business/go/internal/business/types.go)、[响应校验](../../../benchmarks/business/go/internal/loadrun/validate.go)。

### 五个实际测量场景

| 场景 | 正式请求轨迹 | 主要业务工作 | 预期状态 |
|---|---|---|---|
| `catalog` | `GET /api/catalog`，随机租户与分类，固定首屏参数 | 租户及分类过滤、活动商品过滤、价格与主键排序、分类名关联、返回商品卡片 | 200 |
| `detail` | `GET /api/products/{id}`，90% 热集合、10% 全域冷序列 | Redis 查询；未命中时读取商品与规格，编码并写回缓存；框架再次生成 HTTP JSON | 200 |
| `order` | `POST /api/orders`，每单两个不同活动商品，各 1–3 件，唯一幂等键 | 事务内占位订单、锁商品、扣库存、写明细、完成订单、写审计；提交后缓存失效 | 201 |
| `ticket` | `POST /api/tickets/{id}/comments`，使用工单真实所有者身份 | 事务内锁工单、检查归属和状态、写固定中英文评论、递增版本 | 201 |
| `mixed` | 每 10 个请求包含目录 4、详情 3、订单 2、评论 1 | 四种业务共享连接池、缓存与数据库，保留各子场景计数和延迟 | 按子场景判断 |

`catalog` 的真实参数是 `category_id=C&min_price=0&max_price=100000000&cursor_price=0&cursor_id=0&limit=20`。SQL 最多取 21 行，业务层返回 20 行并以最后一条返回记录生成下一页游标。虽然接口支持价格收窄和 `(price_cents,id)` 游标，正式吞吐轨迹没有翻深页，也没有收窄价格范围；这两项能力仅由功能验收覆盖。目录每次直接查 PostgreSQL，不走 Redis。[目录实现](../../../benchmarks/business/go/internal/business/catalog.go)、[SQL 合同](raw/contracts/queries.json)。

`detail` 的热集合为每租户前 100 个活动商品，即本地编号 1–111 中排除 10 的倍数，共 400 个热键。每租户还有 22,400 个活动冷商品，全域冷集合为 **89,600 个键**。对独立详情序号，每十次访问安排九次热键和一次冷键；冷序号先交错租户，再推进各租户内的商品索引，遍历完整个全域冷集合后才复用。混合场景使用独立的详情序号，保持相同 90/10 轨迹；混合块的顺序由固定种子打乱后重复。[轨迹实现](../../../benchmarks/business/go/internal/loadrun/workload.go)、[全域遍历回归测试](../../../benchmarks/business/go/internal/loadrun/workload_test.go)。

90/10 是请求选择规则，不是预先保证的 Redis 命中率。跨过全域冷集合后，先前的冷键可能命中；混合场景中的订单会删除对应详情缓存，热请求也可能未命中。实际比率来自 `X-Benchmark-Cache: hit|miss` 响应头，并随每轮原始计数保存。缓存 TTL 为 3,600 秒；两种语言均采用直接 cache-aside 流程，没有加入不同的 singleflight、分布式锁或框架专属缓存穿透合并算法。缓存中的库存是展示快照，交易库存以数据库事务为准。[缓存业务](../../../benchmarks/business/go/internal/business/catalog.go)、[订单失效逻辑](../../../benchmarks/business/go/internal/business/orders.go)。

缓存失效验收确认的是顺序调用中订单提交后的重新读取。提交后的 `DEL` 与并发详情读取后的 `SETEX` 不是原子协议，较早开始的详情请求可能在删除后回填较旧快照；本实验不提供、也不验证并发库存展示的强一致性保证。新订单是否可成交仍由数据库锁定和条件扣减决定。

`order` 每次生成新的 32 位小写十六进制幂等键，来自本轮标识与请求序号的 SHA256 前 16 字节。业务层按商品主键排序后加锁，以降低交叉锁顺序造成的死锁机会；唯一约束负责同租户、同用户、同幂等键的占位。新两商品订单执行 **9 条客户端业务 SQL**：1 条占位、每商品各 3 条锁定/更新/明细、1 条完成订单、1 条审计；此数不含 `BEGIN/COMMIT`、外键内部检查与 Redis 删除。相同输入的幂等重放返回 200，冲突输入返回 409，但正式吞吐只测新订单的 201 路径，重放 200 不计为负载成功。[订单实现](../../../benchmarks/business/go/internal/business/orders.go)。

`ticket` 按工单的种子归属选择用户，正文为固定中英文业务评论。数据库事务执行锁定工单、插入评论、更新版本三条业务 SQL，保证评论与版本共同提交。这里的负载包含鉴权和事务写入，但不包含评论列表分页、搜索、附件上传、通知发送或工单状态流转。[工单实现](../../../benchmarks/business/go/internal/business/tickets.go)。

## 实验流程与负载模型

### 每轮独立重置和固定预热

专属 PostgreSQL 实例先按数据合同构建种子模板，并以实际 `count(*)` 核对种子规模。每轮停止上一轮应用后，从模板重新克隆业务数据库，清空专属 Redis，再启动本轮框架和网关。订单表大小、库存、评论数和工单版本不跨轮累积。数据库实例与操作系统页缓存没有每轮冷启动，也没有清除宿主页缓存，因此“重置业务数据”不等于“物理磁盘冷读”。[重置实现](../../../benchmarks/business/tools/native.py)。

每轮按照下列顺序准备和测量，步骤及结果写入 `round.json`：[轮次实现](../../../benchmarks/business/tools/run.py)。

1. 启动应用，准备生产配置；PHP 同时编译、核验并归档合同快照。
2. 对全部 400 个热点商品读取两遍，共 800 次请求。第二遍必须全部报告缓存命中；并发预热上限为 24。
3. 对本轮场景按固定 **100 请求/秒运行 10 秒**，共 1,000 个计划请求。预热必须全部成功，出现错误或丢弃立即停止，不进入正式计时。
4. 再次清空 Redis，然后把相同 400 个商品读取两遍，再确认第二遍全部命中。应用进程及其数据库、缓存连接继续保留热状态。
5. 重置 `pg_stat_statements`，执行 30 秒测量发送窗口，随后排空已发请求。
6. 保存负载和资源证据，提取 SQL 统计，执行数据库不变量审计，停止应用与网关，间隔 1 秒后进入下一轮。

固定预热到达率使各配置在同场景预热时执行相同数量的写入，避免较快实现以更多订单或评论扩大测量起点的数据表。预热后的第二次清缓存消除预热冷轨迹留下的缓存，重新建立共同的 400 键热集合。预热产生的订单、评论会保留到本轮正式测量，并在轮后审计中一并核对；其数量对同场景各配置相同。

### 闭环：固定并发下的有效吞吐

闭环使用 64 个请求 worker，前一次请求读取及校验完成后才开始下一次，没有额外思考时间。发送窗口为 30 秒，到期不再主动开始新一轮请求，已发请求继续完成或超时。它回答“这套配置在 64 个并发请求者下能完成多少合法业务请求”，不构成对理论最大吞吐、最佳并发数或长期稳定容量的搜索。[负载实现](../../../benchmarks/business/go/internal/loadrun/runner.go)。

闭环的发起速率会随服务响应变慢而降低，因此闭环 P99 不能代替外部固定流量下的尾延迟。开放与闭合模型的这种区别也见 [Grafana k6 官方说明](https://grafana.com/docs/k6/latest/using-k6/scenarios/concepts/open-vs-closed/)。

### 开放：同场景使用同一个绝对到达率

全部闭环轮结束后，对每一场景分别计算九个配置的三次 `success_rps` 中位数，取其中最小者的 50%，向下取整且至少为 1 请求/秒：

```text
共同到达率(s) = max(1, floor(0.5 × min_v median_r success_rps(v, s, r)))
```

同一场景的九种配置都使用这一绝对到达率，运行 30 秒；最大在途预算为 256。到达率来自完整、有效的闭环矩阵，不能删除慢轮、仅挑最快重复或按每个框架自己的吞吐另定流量。业务错误会降低有效吞吐并保留在校准数据中，不因出现错误而被静默排除。这是一次共同负载比较，不是服务饱和曲线或各自容量 50% 的比较。[调度与校准](../../../benchmarks/business/tools/experiment.py)。

开放调度按 `t0 + i / rate` 安排第 `i` 次请求，独立于前一次请求完成。若在途预算耗尽，计划请求记为 `dropped`；调度落后到发送窗口外，也记录丢弃原因，不延长窗口以悄悄补齐负载。请求主延迟从计划时刻开始，因发生器排队和调度滞后产生的等待进入该延迟。这样避免只按实际发出时间计量而掩盖迟发请求；未发出的请求则作为丢弃显式披露，不伪造延迟样本。[实现](../../../benchmarks/business/go/internal/loadrun/runner.go)、[wrk2 对计划时刻与 coordinated omission 的解释](https://github.com/giltene/wrk2)。

开放到达是固定间隔轨迹，不是泊松到达或突发流量模型。负载发生器自身也有有限 CPU 与校验成本；其调度滞后、丢弃与独立 CPU 证据必须与服务端指标一起阅读，不能自动将所有延迟归因于应用。

### 重复、顺序和汇总

每个负载模型以重复编号 1、2、3 分块，每块含全部 45 个配置与场景组合，块内使用种子 `20261107` 的确定性随机顺序。闭环阶段先于开放阶段；两个阶段各自按相同规则生成调度。各轮重新建库并重启应用，负载参数轨迹由固定种子与请求序号决定，轮次标识区分订单幂等键。[实验调度](../../../benchmarks/business/tools/experiment.py)。

每个指标汇总三轮的中位数及实际最小值、最大值。P99 图表展示的是“三个单轮 P99 的中位数及范围”，不是把请求混合后重新估计的总体 P99。三次重复不足以构造有说服力的总体置信区间，范围也不是置信区间；固定种子减少输入差异，却不能把三轮变成三个独立工作负载分布。[汇总实现](../../../benchmarks/business/tools/render.py)。

## 指标定义与完整性检查

### 请求计数、失败分母和排空

每次已发请求只归入一个结果：成功、HTTP 状态错误、传输错误或语义错误。状态错误包括不符合本场景预期的状态码；语义错误表示状态码符合预期，但 JSON、字段、排序、金额、请求对应关系或必要缓存头不符合合同。只有 HTTP 状态及业务内容同时通过才是成功。传输错误可发生在已接收状态码之后，例如读取正文失败；状态码分布因此不能直接替代互斥结果分类。[响应校验](../../../benchmarks/business/go/internal/loadrun/validate.go)、[计数实现](../../../benchmarks/business/go/internal/loadrun/stats.go)。

正式有效轮必须满足下列守恒关系，并核对总体、子场景、逐秒和直方图计数：

```text
planned = started + dropped
started = completed
completed = success + status_errors + transport_errors + semantic_errors
unsuccessful = planned - success
failure_fraction = unsuccessful / planned
```

开放模型还要求 `expected = ceil(rate × duration)`、`expected = planned`、`not_scheduled = 0`；闭环没有预设到达总数，要求开放速率为 0 且没有开放调度丢弃。取消、未调度请求、直方图溢出、计数缺口或错误的计量时长均不能成为正式有效轮。`check_load_report` 对这些结构错误直接拒绝；正常记录的业务错误、传输失败或开放丢弃则保留，不通过删轮使结果变好。[完整性验证](../../../benchmarks/business/tools/experiment.py)。

失败率以所有计划请求为分母，丢弃与失败均占分母。渲染器的通用表达为 `(planned + not_scheduled)`；正式有效轮的 `not_scheduled` 必须为 0，因此与上式相同。错误图分别展示状态、传输、语义和丢弃类别，各类别的三轮中位数不能相加后当成合并样本的精确失败率。

30 秒为发送窗口，窗口结束后继续等待已发请求结束，最长受单请求 5 秒超时等约束。原始报告同时保留以下三种吞吐，图表主吞吐只使用第一种：

| 字段 | 定义 | 用途 |
|---|---|---|
| `success_rps` | 窗口内成功完成数 / 实际发送窗口秒数 | 正式有效吞吐、开放率校准 |
| `issued_success_rps` | 所有已发请求排空后的成功数 / 发送窗口秒数 | 检查排空完成量，不混作窗口内吞吐 |
| `elapsed_success_rps` | 排空后全部成功数 / 从窗口起点到请求结束的总秒数 | 检查含排空的平均完成速率 |

`drain_seconds` 保留排空时长。逐秒计数中的 `planned` 归于计划时刻，`started` 归于实际 HTTP 开始时刻，`completed` 归于结束时刻；这些事件可能跨秒，所以只要求跨秒加总守恒，不要求每个单独秒桶都满足请求生命周期守恒。[原始统计字段](../../../benchmarks/business/go/internal/loadrun/stats.go)。

### 延迟、分位数与缓存比率

| 延迟字段 | 起止时刻 | 样本范围 |
|---|---|---|
| `latency` | 计划时刻 → 正文读取完成；传输失败则到失败终止 | 全部已完成请求，包含错误 |
| `success_latency` | 计划时刻 → 正文读取完成 | 业务校验成功的请求 |
| `http_duration` | 实际开始 HTTP 调用 → 正文完成或传输失败 | 全部已完成请求 |
| `scheduler_lag` | 计划时刻 → HTTP 开始或丢弃决策 | 全部计划请求，包含丢弃 |

主 P50/P95/P99 来自 `latency`；不能把错误请求从主分位数中移除而只报告成功者的低延迟。丢弃没有正文完成时刻，不伪造主延迟样本，必须同时阅读失败率。JSON 语义校验在正文读取完成之后执行，它消耗负载端 CPU、影响下一次调度与整体吞吐，但不计入本请求 HTTP 耗时。闭环的计划时刻是 worker 开始本次请求时刻，没有外部固定到达队列，不能宣称闭环延迟已经测出了未发生的开放等待。[计时实现](../../../benchmarks/business/go/internal/loadrun/runner.go)。

直方图使用 HdrHistogram 1.4.0，以微秒记录、三个有效数字，输出毫秒制 P50/P95/P99、最大值和平均值，同时保存完整 `hdr_microseconds` 快照、样本数及溢出数。报告校验要求溢出为零，分位数顺序、样本数与分类计数相符。直方图表示的是实际观察到的请求，没有在汇总阶段补造 coordinated omission 修正样本。[直方图实现](../../../benchmarks/business/go/internal/loadrun/stats.go)。

缓存命中率定义为 `cache_hits / (cache_hits + cache_misses)`，分母只包含取得合法缓存状态头的详情响应；无缓存响应的场景显示无此指标，不显示人为的 0% 命中。合法缓存头可能先于正文读取或语义失败被观察到，因此此比率是带缓存标记响应的比率，不能代替业务成功率。混合场景还应结合其独立详情计数阅读。

### CPU 与进程树内存

资源采样围绕**一次完整负载程序调用**进行：启动负载进程后开始采样，等待其退出后结束，时间范围包括负载初始化、发送窗口及排空，应用此前已经预热。实际首末采样时间写入 `resources.json.gz`，它们不与负载内部 30 秒窗口完全重合。应用进程的启动和最初预热不在此 CPU 成本内。图表 CPU 指标为：

```text
每成功请求应用 CPU 毫秒 = 1000 × application.cpu_seconds / 排空后 success
```

分子是应用和 Nginx 全进程树在资源采样范围内的用户态加内核态 CPU 累计增量，分母包含排空成功请求，与窗口内 `success_rps` 的分子不同。它不是单请求墙钟延迟，也不是只截取 30 秒的 CPU 利用率。PostgreSQL 和 Redis CPU 单独保留，未合并进应用指标；负载端另记录 `load_wait4_cpu_seconds`，控制端另记录自身 CPU 增量。[采样实现](../../../benchmarks/business/tools/resources.py)、[调用范围](../../../benchmarks/business/tools/run.py)、[图表口径](../../../benchmarks/business/tools/render.py)。

各进程组的完整性分别判断。负载进程自然退出后，最终进程树采样可能记录 `NoSuchProcess` 或 `ProcessDisappeared`，使 `load.cpu_complete=false`；这不等同于应用、PostgreSQL 或 Redis 组缺失。负载端 CPU 优先读取子进程回收前后资源使用差值 `load_wait4_cpu_seconds`，同时原样保留其轮询采样缺失，不能笼统宣称所有进程组 CPU 都完整。应用 CPU 成本仍只使用 `application` 组自身的完整性判断。

采样间隔为 0.5 秒，包含开始与结束采样。每个样本保存 PID、创建时间、父 PID、进程名称与命令、可读取的 CPU 亲和性、CPU 秒、RSS、PSS 及读取错误。按 PID 与创建时间识别进程，组内去重，不把父进程的累计子进程 CPU 再加一次；组间重复归属会明确报告。读取失败、已观察子进程消失、PID 复用、计数回退和采样缺失均有标记，不能把残缺 CPU 作为更低的成本。

内存指标优先采用**同一次样本中应用与 Nginx 进程树 PSS 之和的最大值**。PSS 将共享页按共享者分摊，RSS 直接求和可能重复计算共享页，两者不能混称。[Linux `/proc` 官方说明](https://www.kernel.org/doc/html/latest/filesystems/proc.html)。只有全部比较轮都有完整 PSS 时图表统一使用 PSS；否则统一回退到明确标记的 RSS。采样缺失或相应数据不完整时，不把局部峰值当作完整峰值排名。

“峰值”指采样时刻观察到的峰值，不能保证捕获两个样本之间的瞬时峰值；完全出生并退出于两个采样点之间的短命进程也可能未被发现。应用启动峰值和长期运行内存增长不在本次测量定义内。原始采样证据比单个汇总数更适合判断这类限制。

## 数据库设计取舍与实证

### 检索精准度、索引过滤与点查成本

共同 schema 使用 PostgreSQL B-tree 主键及必要业务索引，不以移除约束或把所有返回字段塞入索引来优化某一单独场景。[完整 schema](raw/contracts/schema.sql)。主要选择如下：

| 对象 | 结构 | 对当前访问的作用与代价 |
|---|---|---|
| 商品目录 | 部分索引 `(tenant_id, category_id, price_cents, id) WHERE active=true` | 租户、分类等值前缀，价格及稳定游标范围，按价格和主键顺序取少量行；下架商品不占此索引项 |
| 商品点查 | 全局 `id` 主键，再验证租户和活动状态 | 对唯一 ID 定位后检查业务可见性；没有再增加重复的 `(tenant_id,id)` 商品索引 |
| 商品规格 | 主键 `(product_id,position)` | 按商品定位并按规格顺序读取三行，避免独立排序索引 |
| 订单幂等 | 唯一约束 `(tenant_id,user_id,idempotency_key)` | 同一业务作用域的请求去重；每次新订单承担唯一索引检查和写入 |
| 订单明细 | 主键 `(order_id,product_id)` | 订单重放可按商品顺序读取，避免额外订单明细排序索引 |
| 评论时间线 | 索引 `(ticket_id,id)` | 支持按工单读取评论及版本审计；正式写入仍承担维护成本 |

复合 B-tree 的前导等值列与后续范围列决定可限制的扫描区间，这与目录条件的安排一致。[PostgreSQL 18 多列索引说明](https://www.postgresql.org/docs/18/indexes-multicolumn.html)。`(price_cents,id)` 同时解决价格相同的稳定次序与游标边界，避免页码偏移扫描设计；本轮首屏负载只证明当前查询路径，不能以此声称实测了深页性能。

[query-plans.json](raw/query-plans.json) 保存种子数据上的 `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)`：目录实际使用 `bench_products_catalog_idx` 读取 21 行；商品详情按 `bench_products_pkey` 定位；规格按 `bench_specifications_pkey` 读取三行。分类表仅 128 行，规划器在这些计划中选择小表顺序扫描或关联，这不是漏建大表检索索引。计划是准备阶段的只读查询证据，不是每轮动态执行计划全集，也没有对写事务执行会改变数据的 `EXPLAIN ANALYZE`。

主键点查减少需要匹配的记录数，但普通 PostgreSQL 索引扫描仍可能同时访问索引与堆页，并承担可见性检查。本文没有测量 B-tree 物理层高，不能把“主键点查”写成固定一次磁盘 I/O。计划中的 buffer read 也可能由操作系统页缓存满足，不能直接解释为物理磁盘访问。[PostgreSQL 索引扫描说明](https://www.postgresql.org/docs/18/indexes-index-only-scans.html)。

### 更新维护、HOT 机会与存储紧凑度

商品的 `stock` 和较宽的商品描述没有放进目录索引的 `INCLUDE` 列。收益是控制索引体积，并避免库存每次变化都因这个索引列而要求维护索引；代价是目录返回名称和库存仍需读取堆记录，不能用“覆盖索引”描述本设计。商品与工单表使用 `fillfactor=90`，为同行更新保留部分页内空间。

PostgreSQL 的 HOT 优化要求更新不改变相关索引列，且旧行所在页有足够空间。库存、工单版本与更新时间不进入这些表的索引，因此本设计保留 HOT 的可能；较低 fillfactor 则以更多初始页空间换取更新余地。**这只是满足部分前提，不等于本实验已经测得或保证某个 HOT 命中率**，长时间更新仍可能形成新页、死元组与 vacuum 负担。[PostgreSQL 18 HOT 官方说明](https://www.postgresql.org/docs/18/storage-hot.html)。

schema 保留租户、用户、分类、商品、订单和评论关系的外键，以及非负库存、整数金额和状态约束。外键和唯一约束增加写入成本，但避免把缺少一致性保障的路径当作完整交易。订单审计表是被测业务的真实额外写入，不在测量时省略。只保留本合同需要的索引，没有给所有外键自动补建额外索引；当前场景不测父记录删除、批量迁移或所有反向查询，这些工作负载的索引需要另行评估。

[schema-metrics.json](raw/schema-metrics.json) 中，初始全部业务表的 `table_bytes + indexes_bytes` 合计 **156,008,448 字节，约 148.78 MiB**。这是基础表与索引体积，不是 PostgreSQL 整个数据目录、WAL 或运行进程内存。表内 `n_live_tup` 是统计估计，不能用其初始值替代 [seed-counts.json](raw/seed-counts.json) 的实际行数。

### 数据库与缓存运行参数

PostgreSQL `shared_buffers=512MB`、`work_mem=8MB`、`max_connections=96`、`checkpoint_timeout=5min`、`max_wal_size=1GB`。`fsync`、`synchronous_commit`、`full_page_writes` 均开启，未用关闭持久性来抬高交易吞吐。实际读回值保存在 [database-settings.json](raw/database-settings.json)。Redis 上限为 512mb，使用 `noeviction`，关闭快照及 AOF，作为本地缓存而非持久数据源。[配置生成](../../../benchmarks/business/tools/native.py)。

148.78 MiB 的基础关系规模能容纳于配置的 512 MiB 共享缓冲区，但缓存中还会有其他页面，且写入会扩大表及索引。这里没有保证所有页面始终驻留，更没有形成“数据库大于内存”的实验条件。数据克隆、种子准备、预热和宿主页缓存均会影响后续访问；本结果不能宣传为磁盘压力测试、超内存数据库性能或生产容量证明。

### 客户端 SQL 与外键内部统计分开阅读

每轮测量前清零 `pg_stat_statements`，测量及排空后先抓取 SQL 统计，再执行业务审计，避免把轮后审计扫描混入应用 SQL 统计。数据库启用 `pg_stat_statements.track=all` 且关闭 utility 语句统计，原始 `queries.json.gz` 保留 `toplevel`、调用次数、行数、执行时间、共享缓冲命中/读取/写入、临时块与 WAL 计数。[采集实现](../../../benchmarks/business/tools/audit.py)。

**订单的 9 条业务 SQL 指客户端顶层语句，不能把外键触发的内部查询再算成客户端多发了 SQL。** `toplevel=true` 与 `false` 必须分开查看。内部约束检查确实消耗资源，但嵌套执行时间可能已包含在外层语句时间中，直接把全部层级的时间相加会重复计量；也不能把这些时间之和当作事务端到端墙钟延迟。`BEGIN/COMMIT` 等 utility 语句不在这里的业务查询列表中。[PostgreSQL 18 pg_stat_statements 定义](https://www.postgresql.org/docs/18/pgstatstatements.html)。

## 正确性验证与测试边界

### 正式负载前的外部 HTTP 验收

九种配置均通过同一个网关执行相同验收，实际结果位于 [raw/verification/](raw/verification/)。每个配置保存十组检查：新数据库、种子对象、认证、目录、详情缓存与租户隔离、订单提交及重放、并发幂等、事务回滚、输入边界、工单权限与版本。该检查调用真实 HTTP、PostgreSQL 与 Redis，不依赖某个框架自身的测试客户端。[统一验收实现](../../../benchmarks/business/tools/verify.py)。

验收覆盖缺失、过期和篡改令牌，租户不匹配；目录稳定排序、下一页游标、精确价格过滤及空结果；详情先 miss 后 hit、下架与跨租户不可见；订单金额、库存、明细、审计与缓存失效；同键并发只创建一单、改变输入的重放被拒绝；跨租户商品和库存不足时事务整体回滚；非法 JSON、重复成员、类型、正文及媒体边界；工单所有者、关闭状态、评论和版本共同更新。它证明这些合同分支在测量前满足要求，不能替代长期稳定性或任意业务输入的证明。

正式负载中还逐请求检查响应语义，轮后再从数据库独立核对：

- 没有遗留 `pending` 订单，订单审计存在且用户及金额一致。
- 订单总金额等于各明细的整数数量乘单价之和。
- 库存不为负，且等于初始库存减去已提交订单的总销售数量。
- 工单版本等于其评论总数，订单明细没有跨租户商品。
- 数据库提交数不少于客户端已确认的订单、评论数；确认数同时包含预热与正式负载。

数据库可能已经提交而客户端因超时或后续缓存错误未确认成功。审计把多出的订单、评论记为 `unconfirmed_orders`、`unconfirmed_comments`，不把它们追认为成功吞吐，也不删掉对应客户端错误。若客户端确认的提交反而在数据库中缺失，或其他不变量不成立，则保留失败证据并停止实验，不能继续形成完整排名。[独立审计](../../../benchmarks/business/tools/audit.py)。

### 模块测试与覆盖率含义

正式冻结前的模块测试证据和统计范围汇总在 [validation/test-summary.json](validation/test-summary.json)。Go 按源码语句数加权，相同源码块去重后计算；PHP 为 PCOV 实际纳入统计的可执行语句/行覆盖，未测分支覆盖。它们不是统一口径的框架源码覆盖率，也不说明全部依赖或全部业务状态已经覆盖。

| 模块 | 测试覆盖结果 | 主要验证对象 |
|---|---:|---|
| Go 共同业务层 | 309/362，85.36% | 认证、参数、SQL、事务及真实数据库行为 |
| Go 适配器与服务运行层合并 | 509/566，89.93% | 六框架合同、原生绑定、生命周期及服务关闭 |
| Go `loadrun` | 443/482，91.91% | 开闭环计数、超载丢弃、语义校验、确定性轨迹、取消回收、全域冷键遍历 |
| Go 负载 CLI | 49/55，89.09% | 参数、loopback 限制、输出与退出行为 |
| PHP 共享业务 `shared/src` | 229/229，100% | 业务与输入合同、数据库与缓存封装、合同快照 |
| PHP 应用适配层 | 34/34，100% | 控制器、中间件、异常处理及 ThinkPHP provider/service |

Go 适配器与运行层的 89.93% 是合并口径；其中 `internal/serve` 单包为 22/42，52.38%，不能宣称所有 Go 包都超过 80%。PHP 完整测试日志记录 **83 项测试、437 项断言**；原 Clover 合计 263/263，由共享业务 229 与应用适配层 34 构成，不能把 263 全归为共享业务覆盖。薄编译 CLI 与 vendor 不在该分母内。[Go 原始覆盖数据](validation/go-adapters.coverprofile)、[PHP 日志](validation/php-tests.log)、[PHP Clover](validation/php-clover.xml)。

Python 使用 coverage.py 7.17.2。冻结编排工具的结果合并单元测试与既有 pilot 执行覆盖；pilot 在这里提供代码路径覆盖证据，不进入正式性能统计。语句覆盖与分支覆盖分别列示，未测分支的模块不填写人为分支百分比。

| Python 统计范围 | 语句覆盖 | 分支覆盖 | 证据 |
|---|---:|---:|---|
| 冻结编排工具 `audit/native/run/verify`，unit + pilot | 848/899，94.33% | 201/244，82.38% | [原始导出](validation/python-frozen-coverage.json) |
| 实验方法与资源工具 `experiment/resources` | 386/420，91.90% | 未测量 | [原始导出](validation/python-methodology-coverage.json) |
| 运行依赖安装器 `bootstrap` | 332/363，91.46% | 未测量 | [原始导出](validation/python-bootstrap-coverage.json) |
| 图表与证据验证 `render` | 309/311，99.36% | 67/70，95.71% | [原始导出](validation/python-render-coverage.json) |

冻结编排工具的 coverage.py 语句加分支合计值为 91.78%，不能把这个合计数写成纯语句或纯分支覆盖；其 `run.py` 单文件分支覆盖为 71/98，72.45%。上述 Python 文件的语句覆盖均超过 80%，但不代表每个文件的分支覆盖也超过 80%。测试包括完整矩阵、共同速率校准、错误保留、资源去重与读取失败、数据库审计、HTTP 验收、恢复与图表证据验证。

Go 负载相关测试使用 race detector。本实验主动安排的测试与构建在正式测量前完成；宿主上的其他后台活动未被全面观测，不能保证整台机器不存在并行构建或其他资源竞争。测试目录和入口分别见 [Go 测试](../../../benchmarks/business/go/)、[PHP 测试配置](../../../benchmarks/business/php/phpunit.xml)、[Python 工具测试](../../../benchmarks/business/tools/)。部分历史测试没有单独保存 stdout 日志，归档明确区分留存日志、覆盖率原始数据与会话中已观察到的通过结果；覆盖率数据本身不能独立证明测试全部通过。覆盖率是辅助检查，不是性能排名依据；性能结论必须来自完整正式原始数据。

## 原始证据、恢复与复现

### 证据目录的阅读方法

运行程序为每轮使用唯一目录，完整 JSON 采用 gzip 保存。不能只保留图表和一个平均数。以下是正式运行的证据结构及用途：[证据写入](../../../benchmarks/business/tools/run.py)、[渲染前验证](../../../benchmarks/business/tools/render.py)。

| 文件或目录 | 内容与核对目的 |
|---|---|
| `raw/manifest.json` | 模式、冻结源码标识、源码及二进制 SHA256、运行配置摘要、主机信息和协议 |
| `raw/contracts/` | 本次使用的数据合同、SQL、schema、种子与协议原始副本 |
| `raw/seed-counts.json` | 模板中的实际基础行数 |
| `raw/database-settings.json` | PostgreSQL 实际设置，包括持久性开关与缓存大小 |
| `raw/schema-metrics.json` | 基础表、索引定义与字节体积 |
| `raw/query-plans.json` | 三条只读检索的真实执行计划与 buffer 证据 |
| `raw/verification/` | 九种配置的统一外部 HTTP 业务验收 |
| `raw/progress.jsonl` | 阶段和轮次事件时间线，不替代轮次证据 |
| `raw/rounds/<轮次标识>/round.json` | 配置、模型、场景、重复编号、命令、预热、计量摘要、各证据文件摘要 |
| 同轮 `load.json.gz`、`warmup.json.gz` | 正式与预热负载的计数、逐秒统计、直方图、错误样本和计时口径 |
| 同轮 `resources.json.gz` | 分组资源汇总、完整时间范围、逐进程逐样本记录和缺失标记 |
| 同轮 `queries.json.gz`、`audit.json` | 分层 SQL 统计与数据库不变量审计 |
| PHP 轮次的 `contract-snapshot.php` | 实际执行的原始合同数组及其可核对摘要 |
| `raw/arrival-rates.json` | 由完整闭环矩阵导出的五个共同开放到达率 |
| `raw/index.json` | 全部正式轮次的完整索引；完成矩阵后写入 |

图表程序重新核对索引与目录一致性、轮次身份、证据摘要、请求守恒、审计结果、完整 `9×5×2×3` 矩阵以及到达率公式，然后导出逐轮 `rounds.csv`、汇总 `summary.csv`、`summary.json` 和 SVG/PNG 图表。缺失轮、重复轮、混入 pilot、篡改证据或校准不一致都会被拒绝；CPU 或内存观测缺失单独标记，不能以缺失值参与更优排名。

同一输出目录不允许覆盖。`--resume` 仅在原 manifest 与当前生成的 manifest 完全一致时接受恢复，核验范围是清单记录的源码、二进制、协议、配置及环境身份字段。这不能保证可用内存、数据库缓冲区、操作系统页缓存和宿主后台活动相同。已有轮先检查文件摘要，未完成目录移入 `incomplete/` 后从新库重新执行该轮。不能把更改配置后的轮次补进旧实验，也不能用“恢复”名义删除一轮合法但较慢的记录。[恢复规则](../../../benchmarks/business/tools/run.py)。

### 本次中断、有效丢弃与会话恢复

本次正式运行发生过一次会话中断。2026-11-07 21:07:12 UTC 完成的 `open-order-beego-1` 计划 17,130 次请求，发出并成功完成 17,126 次，另外 **4 次因 `schedule_window_elapsed` 未发出**。该轮数据库审计通过，未确认订单和评论均为 0，是保留在正式矩阵中的有效结果。[该轮摘要](raw/rounds/open-order-beego-1/round.json)、[原始负载证据](raw/rounds/open-order-beego-1/load.json.gz)、[审计](raw/rounds/open-order-beego-1/audit.json)。

紧接着的 `open-ticket-fiber-1` 在**正式测量前的初始热点预热**发生 `getresponse()` 超时，控制程序退出。这个未进入正式窗口的尝试与前一轮的 4 次调度丢弃是两件事。已有 **148 轮**完整正式结果经摘要核验后保留；未形成正式结果的目录在恢复时移入 `incomplete/`，其中空目录不会被 Git 单独保存。首段异常堆栈依据当时终端输出作位置摘要，不是保存下来的完整控制台日志。[中断记录](raw/interruptions/session-1/interruption.json)。

期间观察到 `/home/agent` 内存文件系统中的 Go 构建缓存重新增长，迁移前记录的缓存文件合计约 3.46 GB。缓存内容被保留并移动到独立运行目录，可用内存随之发生变化；旧路径随后又出现目录。**缓存增长来源及其与预热超时的因果关系均未确定**，不能将其作为删除慢轮或归因某个框架性能的依据。迁移和观察信息保留在同一中断记录中。

第一次恢复在 PostgreSQL TCP 端口已经可连接、但 SQL 恢复尚未完成时被拒绝，错误为 `database system is not yet accepting connections`。冻结控制器仅等待 TCP 就绪，确实存在这一启动边界。使用同一 PostgreSQL 二进制、配置和 CPU 亲和性等待恢复完成并正常停止后，21:24:35 UTC 以原源码、原二进制和原协议再次恢复；全部旧轮经校验复用，未改写旧 manifest。重新开始的 Fiber 工单轮于 21:27:45 UTC 完成，所有计划请求成功；完整矩阵于 22:54:23 UTC 完成，恢复进程正常退出。[首次恢复失败日志](raw/interruptions/session-1/formal-resume-1.log)、[数据库恢复日志](raw/interruptions/session-1/postgres-recovery-1.log)、[完整恢复会话日志](raw/interruptions/session-1/formal-resume-2.log)。

恢复重新启动了 PostgreSQL 和 Redis。每轮仍按原协议重置业务数据并预热，但基础设施缓冲区、操作系统页缓存及可用内存不保证与中断前相同。因此本实验保留完整时间线和三轮范围，不能宣称 270 轮来自没有中断、没有环境状态变化的连续运行。

### 从冻结依赖构建复现实验

复现需使用 Linux x86_64、Python 3.12 及其 `pip`，以非 root 用户运行 PostgreSQL。主机应提供 `curl`、C/C++ 编译器、`make`、`perl`、`pkg-config`、`git`、`unzip`、`taskset`，以及 OpenSSL、zlib、PCRE2、curl、GMP、zstd 开发库；安装入口会验证构建工具与开发库前置条件。协议需要 CPU 编号 0–8 均可用，以及其中指定的本地端口未占用。若改变 CPU 拓扑、版本、时长或工作负载，应视为新实验并保存新 manifest。[安装前置检查](../../../benchmarks/business/tools/bootstrap.py)、[协议](../../../benchmarks/business/contracts/protocol.json)。

以下命令从仓库根目录开始，为复现使用新的独立运行与结果目录。下载及安装在性能测量前完成；安装器按归档和 wheel 摘要核验依赖，Composer 使用锁文件，不执行依赖升级。运行目录由实验所有权标记保护，数据库和 Redis 仅绑定 loopback。示例只创建独立复现输出，不覆盖本报告归档。

新克隆应先取得前述远端源码分支，并检出固定源码提交，再执行下方构建与运行步骤：

```bash
git clone --branch bench/real-business-20261107 \
  https://github.com/zhuhanxin0308/thinkgo.git thinkgo-business-repro
cd thinkgo-business-repro
git checkout --detach 6f332ab0122831ed75044aa63910aa2b57947b34
```

```bash
export BENCH_REPOSITORY="$(pwd)"
export BENCH_ROOT="$BENCH_REPOSITORY/benchmarks/business"
export BENCH_RUNTIME="$BENCH_REPOSITORY/../benchmark-runtime-repro"
export BENCH_RESULTS="$BENCH_REPOSITORY/../benchmark-results-repro"

python3 "$BENCH_ROOT/tools/bootstrap.py" --runtime "$BENCH_RUNTIME"

export PATH="$BENCH_RUNTIME/go-1.27.3/bin:$BENCH_RUNTIME/php/bin:$PATH"
export PYTHONPATH="$BENCH_RUNTIME/python-packages${PYTHONPATH:+:$PYTHONPATH}"
export GOCACHE="$BENCH_RUNTIME/go-build-cache"
export GOTOOLCHAIN=local
export GOMAXPROCS=2
export CGO_ENABLED=1
export GOAMD64=v1
mkdir -p "$BENCH_ROOT/bin"
cd "$BENCH_ROOT/go"
go mod download
go build -trimpath -mod=readonly -p=2 -o "$BENCH_ROOT/bin/" ./cmd/...
cd "$BENCH_REPOSITORY"

python3 "$BENCH_ROOT/tools/run.py" \
  --runtime "$BENCH_RUNTIME" \
  --work "$BENCH_RUNTIME/work/business" \
  --output "$BENCH_RESULTS/raw" \
  --mode measure

python3 "$BENCH_ROOT/tools/render.py" \
  --input "$BENCH_RESULTS/raw" \
  --output "$BENCH_RESULTS/charts"
```

正式二进制的构建信息见 [final-build-info-v2.json](validation/final-build-info-v2.json)：Go 1.27.3、`-trimpath`、`CGO_ENABLED=1`、`GOAMD64=v1`，并嵌入本地冻结提交的 VCS 信息。上面的复现命令保留这些构建设置；远端同树提交的 VCS 标识和时间不同，系统编译器及链接环境也可能不同，因此不承诺新构建与本次归档二进制逐字节同一摘要。复现结果必须记录自己的实际二进制和环境身份。

`measure` 自带九种配置的 HTTP 验收，然后依次执行全部闭环及开放矩阵；正式模式拒绝只选择部分配置，也要求 `benchmarks/business` 没有未提交变更。`verify` 模式仅做功能验收；`pilot` 使用较短的预热和测量，作为链路检查，不能用于正式校准或性能结论。使用这些模式时应指定不同输出目录，防止身份混合。每个运行会话内，服务由同一控制程序统一创建和回收，避免将不同执行环境的本地端口误当成同一网络命名空间。

正式复现前应结束编译、测试及其他 CPU/内存密集工作，并把 Go 构建缓存放在有足够空间的磁盘路径；驻留于内存文件系统的大型构建缓存会改变可用内存条件。复现结果应保留新的硬件与运行清单，即使工作负载完全相同，也不能假定不同主机必然重复同一吞吐数字。

## 结果适用范围

这份实验可以回答：在公开的合成业务合同、共同数据规模、连接预算、本地网关与固定运行配置下，各实现的合法业务吞吐、共同开放负载尾延迟、应用 CPU 成本、进程树采样内存、错误及缓存命中表现如何。比较时应同时看有效吞吐、尾延迟、错误、资源与波动，不能仅凭某一条最快数值决定通用优劣。

需要保留的边界包括：

- 每轮只有 30 秒，不能证明长时间稳定性、内存泄漏不存在、检查点与 vacuum 的长期代价，或进程滚动更新表现。
- 单个固定并发点与一个共同开放到达率没有覆盖容量曲线、突发流量、排队拐点及不同超时策略。
- 数据可容纳于配置的数据库缓存，目录为首屏、冷键有限、商品库存充足；结果不覆盖超内存随机读、深分页、抢购库存热点或大订单。
- 四租户和确定性均匀选择不同于真实的高度倾斜用户分布；混合请求会产生共享缓存及写入相互作用，但没有完整用户会话链。
- 本地回环连接、相同 SQL 和共享业务实现控制了部分变量，也有意不比较各框架惯用 ORM 与更复杂生态；驱动及运行时差异仍存在。
- CPU 亲和性不能提供宿主独占保证，采样内存不能替代连续峰值监测，三次重复范围不能替代总体置信区间。

框架选择还涉及开发与维护成本、团队熟悉度、部署约束、功能生态和可靠性要求。这些因素没有用本次性能数字量化，不能从请求每秒的排序直接推导。
