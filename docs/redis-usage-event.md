# Redis 用量事件

ccLoad 可以把代理请求用量异步发布到 Redis。该能力默认关闭，不参与请求授权，也不改变代理返回结果。

## 配置

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `CCLOAD_REDIS` | 空 | Redis DSN；为空时使用 Noop Publisher |
| `CCLOAD_REDIS_EVENT_STREAM` | `ccload:usage` | 默认 Stream 或 Pub/Sub channel |
| `CCLOAD_REDIS_EVENT_STREAM_PATTERN` | 空 | 环境路由模板，例如 `ccload:{env}:usage` |
| `CCLOAD_REDIS_EVENT_MODE` | `stream` | `stream` 使用 `XADD`，`pubsub` 使用 `PUBLISH` |
| `CCLOAD_REDIS_EVENT_BUFFER` | `1024` | 异步事件队列容量 |

当 Auth Token 描述符合 `sedna-<environment>-user-<uid>` 时，事件会携带 `environment`。配置 Stream Pattern 后，`sedna-dev-user-1` 会路由到 `ccload:dev:usage`。不符合该约定的 Token 继续使用默认 Stream。

## 事件语义

- `kind=attempt`：一次真实上游尝试，是 usage 和成本的账本事件。只有对应日志批量落库成功后才发布。
- `kind=request`：用户请求的终态导航事件，只包含最终状态、总耗时和尝试次数，不包含 usage 或成本。
- 两类事件通过 `request_id` 关联。消费端只能汇总 `attempt` 的 usage 和成本，不能把 `request` 重复计入。
- request 事件可能早于 attempt 到达；Redis 或日志写入失败时也可能只有其中一类事件。

## 499 计费

客户端取消请求并且上游已经返回 usage 时：

- 日志和 attempt 事件保留真实 Token 与成本；
- Auth Token 的 Token/费用累计照常增加；
- `success_count` 和 `failure_count` 均不增加，避免客户端取消影响渠道健康度。

上游直接返回的 499 不视为客户端取消，不适用上述中性计费。

## 可靠性边界

- 事件发布使用有界内存队列，不阻塞代理主链路；队列满时丢弃事件并记录采样告警。
- Redis 投递失败不会改变客户端响应或日志落库结果。
- 进程关闭时先等待日志 Worker 刷盘，再排空事件队列并关闭 Redis 连接。
