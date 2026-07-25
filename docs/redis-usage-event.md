# ccLoad Redis 用量事件 —— 设计文档

> 状态:设计定稿,待实现
> 范围:在请求结束、用量确定后,复用已归一化的 usage/cost,向 Redis 发布用量事件。

## 1. 目标与原则

在请求结束、用量已确定后,复用**已经归一化好的** usage/cost,向 Redis 发布用量事件。不新增第二套解析或计价。

- **单一归一化来源**:事件字段全取自已构建的 `LogEntry` + 少量透传上下文,不再调 `computeRequestCost` / usage 解析。
- **落库后触发**:`attempt` 事件由 `LogService` 在**批量写库成功后**发布,与"日志成功落库"严格对齐;DB 写失败则不发,保证事件 ≈ 账单真值。
- **Fail-open 不阻塞**:Redis 未配置/不可用时代理主链路零影响;事件走独立有界队列 + worker,满则丢弃计数(复用 `tokenStatsCh` 降级范式)。
- **区分用户请求与上游尝试**:同一 `request_id` 下,每次真实上游调用发 `kind=attempt`,请求收尾发一条 `kind=request`(瘦汇总)。

## 2. 现状基线(方案的事实依据)

一次代理请求结束时,归一化结果已在两处各算一遍,数据源都是 `fwResult`(`res`):

| 归一化产物 | 现有位置 | 复用方式 |
|---|---|---|
| usage 拆解(in/out/reasoning/cache 5m/1h) | `proxy_util.go:buildLogEntry` 写进 `LogEntry`;`updateTokenStatsAsync` 读 `res.*Tokens` | 已解析完毕,事件不再解析 |
| 标准成本 | `computeRequestCost(billingModel, tier, res)+res.ToolCostUSD`(日志);`computeRequestCost(actualModel,…)`(统计) | 复用日志口径(`entry.Cost`)这一份 |
| 倍率后成本 | `applyTokenStatsUpdate` 里 `costUSD*multiplier` | 事件 `Effective = entry.Cost * entry.CostMultiplier` |

终态处理点(每个都是"一次真实上游尝试"结束)只有四个,全部已调用 `logProxyResult`:
`handleProxySuccess`(proxy_error.go:464)、`handleProxyErrorResponse`(:531)、`handleNetworkError`(:211)、`handleStreamingErrorNoRetry`(:494)。

用户请求级终态在 `writeFinalProxyResponse`(proxy_handler.go:526,全失败汇总日志)与 `runProxyAttemptLoop` 成功时的 `return nil, true`。

**关键结论**:日志在"流结束/连接终止之后"才写(流式 usage 此时才落 `fwResult`)。把事件挂到**日志落库成功之后**,天然满足"流式必须等流结束再发最终事件""用量已确定后再生成事件"。

## 3. 决策记录

### 决策一:499 按实际 usage 计费(a+b+c,不动健康度 d)

**背景 —— 499 有两种来源**:
- 客户端取消(`isClientCanceled=true`):流式下上游可能已吐部分 token → `res` 有真实 usage。
- 上游返回 499(映射为对客户端 502):通常无真实 usage。
两者在 `logs` 里都记 `status_code=499`,SQL 层无法区分。

**"计费"是 5 件可拆分的事**:
| # | 动作 | 落点 |
|---|---|---|
| a | 记 token 数 | `logs.*_tokens`、`token_stats` |
| b | 记成本 | `logs.cost`、`token_stats.cost` |
| c | 计入每日限额 / auth_token 费用限额 | `CostCache` + `auth_tokens.effective_cost` |
| d | 计成功/失败健康度 | `token_stats` 成/败计数、`logs` 成功率 |
| e | Redis 事件 cost | 事件流 |

**决策**:仅对**客户端取消且 `hasConsumedTokens(res)==true`** 的 499 做 **a+b+c**;**不做 d**(复用 596 语义:计费口径但不计健康度)。上游返回的 499 无 usage,自然 cost=0。

**关键连带 —— 统计口径非对称**:全仓库对 499 有一条深层不变量——**计数类**统计(成功率、错误数、总调用、RPM、趋势图、健康度排序)系统性 `status_code != 499` 排除(见 `auth_token_stats.go`、`metrics.go`、`metrics_filter.go`、`log.go`、`store_impl.go`);但 **token/花费 SUM 不带该过滤**(`metrics.go:38-43`、`GetTodayChannelCosts` metrics.go:929),现在能对齐纯因 499 行 token/cost 恒为 0。

一旦给 499 写入 token/cost:
- 统计页的 **token 总量 / 花费 / effective_cost / 每日限额** 会纳入 499,而 **调用数 / 成功率 / RPM** 仍排除 499。
- **刻意接受此非对称**(取消确实消耗了算力,应计费防薅),UI 注明"含已取消请求的消耗"。
- `CostCache` 内存累加与重启恢复 `GetTodayChannelCosts` 口径天然一致(两条都不过滤 499)。
- **健康度 d 绝不改动**:499 排除出成功率是刻意设计("坏客户端别把好渠道打残"),`updateTokenStatsAsync` 对 499 走"计费但成/败计数都不 +1"的分支(参照 596)。

### 决策二:`request` 事件做瘦汇总,请求收尾即发

**背景 —— 收尾处拿不到 usage**:`runProxyAttemptLoop` 成功时 `return nil, true`(proxy_handler.go:364),**胜出 attempt 的 `fwResult` 已被丢弃**,`HandleProxyRequest` 收尾处只有 `originalModel / isStreaming / reqCtx`。

**决策**:`request` 事件只带 `request_id / 最终 status / 总耗时 / attempt 数 / model / token 标识`,**不带 usage/cost**;计费权威完全由 `attempt` 事件承载(天然落库后发)。

由此消费端契约:
- **attempt = 账本**(唯一计费权威,落库后发,账实一致);**request = 导航视图**(收尾即发,不经落库门槛)。
- 二者按 `request_id` 关联;**不得**对两者同时求和(避免重复计数)。
- 允许 `request` 先于其 `attempt` 到达(attempt 有批量 flush 延迟),消费端不假设顺序。
- 允许"有 request 无 attempt"(日志队列满/落库失败时 attempt 不发,request 照发)——request 仅作 advisory。

## 4. 数据模型

新增 `internal/model/usage_event.go`:

```go
type UsageEventKind string
const (
    UsageEventAttempt UsageEventKind = "attempt" // 单次真实上游调用(计费权威)
    UsageEventRequest UsageEventKind = "request" // 用户请求终态汇总(瘦视图)
)

type UsageEvent struct {
    RequestID   string
    AttemptSeq  int            // request 事件为 0
    Kind        UsageEventKind
    Time        JSONTime
    TokenHash   string
    AuthTokenID int64
    ChannelID   int64
    Model       string
    ActualModel string
    StatusCode  int
    IsStreaming bool
    Duration    float64
    FirstByte   float64
    ClientIP    string

    // 以下仅 attempt 事件填充(request 事件留零)
    InputTokens, OutputTokens, ReasoningTokens     int
    CacheReadInputTokens, CacheCreationInputTokens int
    Cache5mInputTokens, Cache1hInputTokens         int
    StandardCostUSD  float64 // = LogEntry.Cost
    CostMultiplier   float64 // = LogEntry.CostMultiplier
    EffectiveCostUSD float64 // = Cost * CostMultiplier
    ServiceTier      string
}
```

`LogEntry` 加瞬态字段(仿 `DebugData`,不落库):
```go
UsageEvent *UsageEvent `json:"-"` // 落库成功后发布
```

## 5. 发布链路

新增 `internal/eventbus/`:
- `publisher.go` —— `Publisher` 接口 + `NoopPublisher`(`CCLOAD_REDIS` 为空时装配,全链路等价关闭)。
- `redis.go` —— `RedisPublisher`,`go-redis/v9`,默认 `XADD`(Stream,可回溯/消费组),可切 Pub/Sub;序列化用 sonic。
- `worker.go` —— 有界队列 + worker,与 `tokenStatsWorker` 同构(优雅关闭 drain,加入 `s.wg`)。

配置(仿 `CCLOAD_MYSQL` 风格,Fail-Fast):

| 环境变量 | 默认 | 说明 |
|---|---|---|
| `CCLOAD_REDIS` | 空 | DSN,如 `redis://:pass@host:6379/0`;空 = 装配 Noop,整链关闭 |
| `CCLOAD_REDIS_EVENT_STREAM` | `ccload:usage` | Stream/channel 名 |
| `CCLOAD_REDIS_EVENT_MODE` | `stream` | `stream`(XADD)/ `pubsub` |
| `CCLOAD_REDIS_EVENT_BUFFER` | 沿用 `DefaultTokenStatsBufferSize` 量级 | 事件队列容量 |

## 6. 改动清单(逐文件)

**A. 事件模型与依赖**
1. 新建 `internal/model/usage_event.go`(见 §4)。
2. `internal/model/log.go` —— `LogEntry` 加 `UsageEvent *UsageEvent \`json:"-"\``。
3. `go.mod` —— 加 `github.com/redis/go-redis/v9`(不影响 `-tags sonic`)。
4. 新建 `internal/eventbus/{publisher,redis,worker}.go`(见 §5)。

**B. 事件构建(复用归一化)**
5. `internal/app/proxy_util.go`:
   - `logEntryParams` 加 `TokenHash / RequestID / AttemptSeq`。
   - `buildLogEntry` 末尾:用已算好的 `entry.Cost / entry.*Tokens / entry.CostMultiplier` 直接组装 `entry.UsageEvent`(`Effective = Cost * CostMultiplier`),**不重算**。
6. `internal/app/proxy_error.go:139 logProxyResult` —— 从 `reqCtx` 透传 `tokenHash / requestID / attemptSeq` 到 `logEntryParams`。
7. `internal/app/proxy_util.go` —— `proxyRequestContext` 加 `requestID string` 与 attempt 计数;`proxy_handler.go` 建 `reqCtx` 处(约 :337)生成 `requestID`,每进入一次渠道尝试自增 `AttemptSeq`。

**C. 499 计费(决策一)**
8. `internal/app/proxy_error.go:534`(`handleProxyErrorResponse`)与 `:226`(`handleNetworkError`)—— 放开 `if status != 499`,改为 `499 && isClientCanceled && hasConsumedTokens(res)` 时也调 `updateTokenStatsForProxy`。
9. `internal/app/proxy_error.go:340 updateTokenStatsAsync` / `applyTokenStatsUpdate` —— 为 499 引入"计费但不计健康度"分支(参照 596:`AddCostToCache` 照常,`UpdateTokenStats` 成/败计数两边都不 +1)。
10. 复核 `buildLogEntry` 错误分支:499 客户端取消时需把 `res` 的 token/cost 写入 `logs`(当前 error 分支不填 token),使 `logs.cost` 与 `token_stats` 一致。

**D. 发布触发**
11. `internal/app/log_service.go` —— `LogService` 加 `publisher eventbus.Publisher`;`flushLogs` 写库 `err==nil` 分支内,遍历 batch 对 `entry.UsageEvent != nil` 调 `publisher.Publish`(非阻塞)。
12. `internal/app/proxy_handler.go:363-368`(`HandleProxyRequest` 收尾)与 `writeFinalProxyResponse` —— 构建 `kind=request` 瘦汇总事件并 `publisher.Publish`(不经落库门槛)。
13. `internal/app/server.go:NewServer`(约 :183,`tokenStatsCh` 初始化附近)—— 按 `CCLOAD_REDIS` 装配 `Publisher` 并注入 `LogService`;worker 入 `s.wg`,shutdown drain 排在日志 flush 之后。

**E. 文档**
14. `CLAUDE.md` —— 「计费与限额」补 499 计费口径;新增一段「用量事件(Redis)」说明 attempt=账本 / request=视图、落库后发、fail-open。

## 7. 事件语义矩阵

| 场景 | attempt 事件 | request 事件 |
|---|---|---|
| 成功 2xx | full usage + cost | status=2xx,无 usage |
| 上游错误(4xx/5xx/597/598/599) | token/cost=0 + status | 最终 status |
| **客户端取消 499(已消耗)** | **真实 usage + cost**(不计健康度) | status=499 |
| 上游返回 499(无 usage) | cost=0 | 映射后 status(502) |
| 多渠道/多 Key 重试 | 每次真实调用一条,AttemptSeq 递增 | 收尾一条汇总 |
| 全渠道失败 | 各 attempt | finalStatus,无 usage |
| Redis 宕机/未配置 | 入队丢弃计数,主链路零影响 | 同左 |

## 8. 测试与验收

- `internal/eventbus`:Noop 装配、`miniredis` 跑 stream/pubsub、队列满降级、Close drain。
- 集成(仿 `billing_integration_test.go`):断言 **2xx / 上游错误 / 499-已消耗 / 多渠道重试** 四类下,attempt 事件的 usage/cost 与落库 `LogEntry` **逐字段一致**(证明复用同源);499 计费进了 `token_stats`/`CostCache` 但**未**改成功率。
- 回归:`go test -tags sonic ./internal/...`、`make race-fast`、`golangci-lint run ./...` 零警告。协议无关,不涉及 `sync-cliproxy-core` 快照。

## 9. 落地顺序

1. `model.UsageEvent` + `LogEntry` 瞬态字段。
2. `eventbus`(Noop+Redis+worker)+ go.mod。
3. `buildLogEntry` / `logProxyResult` / `reqCtx` 事件构建 + request_id/attempt。
4. `server.go` 装配 + `LogService` 落库后发布 + 收尾发 request 事件。
5. 499 计费改造(C 组)+ 对应测试。
6. 文档 + 集成/回归。
