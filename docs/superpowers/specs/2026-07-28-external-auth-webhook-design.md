# ccLoad 外部请求鉴权 Webhook 设计

## 1. 背景与目标

ccLoad 当前使用本地 API Token 对模型请求进行认证，并基于该 Token 执行模型白名单、渠道限制、费用限额、并发限制、日志归属和用量统计。

本设计增加可选的外部鉴权 Webhook。启用后，客户端使用外部平台的 `Authorization` 凭证访问 ccLoad；ccLoad 在请求上游模型之前调用外部平台验证用户。外部平台返回用户 UUID、对应的 ccLoad API Token 和授权过期时间，ccLoad 再复用现有本地认证与授权逻辑。

目标：

- 外部平台成为用户身份的认证方。
- ccLoad 继续作为模型、渠道、费用和并发权限的最终授权方。
- 客户端通过 `X-Sedna-Env` 选择管理员已配置且启用的鉴权环境，ccLoad 按环境调用对应的 authz URL。
- 普通 HTTP、SSE 和 Responses WebSocket 模型请求使用一致的鉴权语义。
- 外部用户 UUID 进入请求日志和 Redis 用量事件，便于外部平台结算关联。
- 通过来源 IP/CIDR 白名单支持旧调用方渐进迁移。
- 外部鉴权失败时 Fail-closed，不请求上游、不产生用量费用。

非目标：

- 不让外部平台动态返回模型、渠道或额度限制。
- 不缓存外部鉴权结果。
- 不使用 Nginx `auth_request` 代替应用内鉴权。
- 不修改 ccLoad 现有 API Token 数据模型和权限规则。
- 不记录或持久化客户端 Authorization、Webhook 返回的 ccLoad Token。

## 2. 总体架构

新增两个边界清晰的组件：

### 2.1 ExternalAuthService

只负责外部身份认证：

- 读取已经生效的外部鉴权配置。
- 读取并校验当前请求中唯一的 `X-Sedna-Env`，查找对应的启用环境配置。
- 构造 Webhook 请求。
- 从当前用户请求提取原始 `Authorization`。
- 强制设置 `X-Original-Authorization`。
- 执行超时、重试、退避和状态码判断。
- 解析并验证 Webhook 响应头。
- 返回外部用户 UUID、ccLoad Token 和授权过期时间。

它不查询 ccLoad Token 数据，不执行模型、渠道、额度或并发授权。

### 2.2 ExternalAuthMiddleware

用于普通 HTTP/SSE 模型入口：

1. 获取经过可信代理规则解析的客户端 IP。
2. 命中迁移白名单时跳过 Webhook，继续使用现有客户端 ccLoad Token 鉴权。
3. 未命中白名单时强制要求非空 `Authorization`。
4. 调用 `ExternalAuthService`。
5. 将返回的身份信息写入仅服务端可见的请求上下文。
6. 调用现有 `RequireAPIAuth` 校验返回的 ccLoad Token。
7. 继续执行现有模型、渠道、费用和并发限制。

`RequireAPIAuth` 增加受控的内部 Token 入口：外部鉴权成功时优先读取上下文中的临时 ccLoad Token；外部鉴权关闭或白名单绕过时保持现有凭证提取行为。

## 3. 请求链路

### 3.1 普通 HTTP 与 SSE

```text
用户请求 Authorization
  → 解析可信客户端 IP
  → 迁移白名单判断
  → 校验 X-Sedna-Env 并选择对应环境的 authz URL
  → ExternalAuthService 调用 Webhook
  → 获取 X-User-Id / X-Ccload-Token / X-Authz-Token-Exp
  → AuthService 校验返回的 ccLoad Token
  → 本地模型、渠道、额度、并发限制
  → 选择渠道并请求上游
  → 日志与 Redis 事件携带 external_user_id
```

每个模型请求实时鉴权一次，不缓存结果。

### 3.2 Responses WebSocket

WebSocket 握手必须携带原始 `Authorization` 和唯一的 `X-Sedna-Env`。ccLoad
校验环境配置后，将原始 Authorization 和规范环境标识安全保存在连接上下文中，
不记录、不回显。

每个 `response.create`：

1. 使用握手时保存的原始 Authorization 和规范环境标识选择并调用 Webhook。
2. 使用当前 `response.create` 的模型信息构造鉴权元数据。
3. 校验响应头和授权过期时间。
4. 使用返回的 ccLoad Token执行本地 Token、模型、渠道、额度和并发校验。
5. 将本次外部用户 UUID 和 ccLoad Token身份绑定到本次请求尝试及其用量。

同一 WebSocket 连接中，不同 `response.create` 可以返回不同用户或 ccLoad Token；每次用量归属各自最新鉴权结果。握手成功不代表后续消息永久获得授权。

## 4. 生效范围

执行外部鉴权：

- `/v1/*` 下的实际模型请求。
- `/v1beta/*` 下的实际模型请求。
- `/backend-api/codex/responses`。
- HTTP、SSE、Responses WebSocket。
- `/v1/messages/count_tokens` 等需要 API 认证的模型相关特殊端点。
- WebSocket 的每个 `response.create`。

不执行外部鉴权：

- `/health`
- `/public/*`
- `/login`、`/logout`
- `/admin/*`
- `/dashboard/*`

外部鉴权关闭时，所有路由保持现有行为。

## 5. Webhook 请求契约

### 5.1 请求

ccLoad 使用 `POST` 调用 `X-Sedna-Env` 对应环境配置中的 authz URL：

```http
POST https://sedna-dev.example.com/internal/llm/authz
Content-Type: application/json
X-Original-Authorization: <用户当前请求的原始 Authorization 值>
X-Sedna-Env: develop
```

请求体：

```json
{
  "request_id": "req_xxx",
  "method": "POST",
  "path": "/v1/responses",
  "model": "gpt-5.4",
  "stream": true,
  "client_ip": "203.0.113.10"
}
```

规则：

- `X-Original-Authorization` 逐字取自用户当前请求的 `Authorization`。
- 忽略并覆盖客户端主动提交的 `X-Original-Authorization`。
- 未命中迁移白名单时，`X-Sedna-Env` 必须恰好出现一次；首尾空白清理后按小写环境标识精确匹配。
- ccLoad 不盲目透传客户端的 `X-Sedna-Env`，而是用已匹配配置的规范环境标识覆盖出站请求头。
- 缺失、重复、非法、未配置或已停用的环境统一返回 `403 Forbidden`，不暴露已配置环境列表，且不调用 authz 或模型上游。
- 启用外部鉴权且未命中迁移白名单时，缺少 `Authorization` 返回 `401`。
- 不从 `X-API-Key`、`x-goog-api-key` 或 Gemini `?key=` 转换外部身份。
- 不发送提示词、消息、文件、工具参数或完整请求正文。
- 禁止自动跟随 HTTP 重定向，防止 Authorization 被转发到其他地址。
- 请求和响应敏感 Header 不进入访问日志、调试日志或错误信息。

### 5.2 成功响应

成功使用任意 `2xx` 状态，并必须返回：

```http
X-User-Id: <用户 UUID>
X-Ccload-Token: <用户对应的 ccLoad API Token>
X-Authz-Token-Exp: <JWT 过期 Unix 秒>
```

校验：

- `X-User-Id` 必须是合法 UUID。
- `X-Ccload-Token` 必须是非空字符串，并通过现有 `AuthService` 校验。
- `X-Authz-Token-Exp` 必须是 Unix 秒整数。
- `exp <= 当前 Unix 时间 + 5 秒` 时视为已过期。
- 任一字段缺失或格式错误都视为鉴权服务响应异常。
- Webhook 正文不参与业务判断，最多读取或丢弃 16 KiB。

`X-Ccload-Token` 不返回客户端、不写日志、不落库、不进入 Redis。

### 5.3 状态码

| Webhook 结果 | ccLoad 行为 | 重试 |
|---|---|---|
| 2xx 且响应头合法 | 继续本地 ccLoad Token授权 | 否 |
| 401 / 403 | 向客户端返回 403 | 否 |
| 408 / 429 / 5xx | 临时错误，最终向客户端返回 503 | 是 |
| 其他 4xx | 配置或请求错误，向客户端返回 503 | 否 |
| 网络错误 / 超时 | 最终向客户端返回 503 | 是 |
| 2xx 但响应头非法 | 向客户端返回 503 | 否 |
| 返回的 ccLoad Token无效、禁用或过期 | 向客户端返回 503 | 否 |

Webhook 响应正文不透传客户端。

## 6. 超时与重试

- 单次调用总超时：默认 2000 ms。
- 首次失败后最多重试 2 次，即最多调用 3 次。
- 第一次退避约 100 ms，第二次约 300 ms，并加入少量随机抖动。
- 只重试网络错误、超时、408、429 和 5xx。
- 明确拒绝、确定性 4xx、非法成功响应不重试。
- 最坏额外延迟约 6.4 秒。
- 三次均失败后 Fail-closed。

鉴权失败时：

- 不选择上游渠道。
- 不占用 ccLoad Token 并发槽。
- 不发送上游请求。
- 不产生 Token 用量或费用。

## 7. 迁移白名单

新增 `external_auth_bypass_cidrs`，支持逗号分隔的单 IP 和 CIDR：

```text
10.0.0.0/8,192.168.1.20/32,203.0.113.45/32
```

规则：

- 使用 Gin 在 `TRUSTED_PROXIES` 配置下解析的客户端 IP。
- 不直接信任任意客户端提供的 `X-Forwarded-For`。
- 命中白名单时只跳过外部 Webhook，仍执行原有 ccLoad Token鉴权。
- 未命中时强制走外部鉴权。
- 非法 IP/CIDR 配置拒绝保存。
- 每次绕过记录安全审计日志，但不记录任何 Token。
- 设置页面明确提示该能力仅用于迁移，迁移完成后应清空。

不支持按 Host 或访问 IP/域名方式绕过，避免伪造 Host 导致鉴权绕过。

## 8. 系统设置

新增“外部请求鉴权”分组：

| 设置 | 默认值 | 说明 |
|---|---:|---|
| `external_auth_enabled` | `false` | 总开关 |
| `external_auth_timeout_ms` | `2000` | 单次调用总超时 |
| `external_auth_max_retries` | `2` | 首次调用后的最大重试次数 |
| `external_auth_bypass_cidrs` | 空 | 迁移绕过来源 IP/CIDR |

环境路由使用独立的 `external_auth_environments` 表，不将多条结构化配置塞入单个字符串设置：

| 字段 | 说明 |
|---|---|
| `id` | 内部主键 |
| `environment` | 唯一环境标识，例如 `develop`、`test` |
| `authz_url` | 该环境的完整 HTTPS authz URL |
| `is_active` | 是否允许客户端选择该环境 |
| `created_at` / `updated_at` | 审计时间 |

环境标识只允许小写字母、数字、`-` 和 `_`，不能为空且全表唯一。管理后台支持新增、编辑、启用、停用和删除环境配置。删除或停用后，新请求立即不能再选择该环境。

专用管理 API：

- `GET /admin/external-auth/environments`
- `POST /admin/external-auth/environments`
- `PUT /admin/external-auth/environments/:id`
- `DELETE /admin/external-auth/environments/:id`

写操作复用现有管理员认证和请求校验。删除不存在的记录返回 `404`；环境名冲突
返回 `409`；非法环境名或 URL 返回 `400`。启用总开关时若没有有效环境，更新
请求返回 `400`，保留原有关闭状态。

校验：

- 开启时必须至少存在一个启用且 URL 合法的环境。
- 每个 authz URL 必须为合法 HTTPS URL。
- 超时范围为 100～10000 ms。
- 重试次数范围为 0～5。
- 白名单逐项解析，任一非法值都拒绝保存。
- authz URL 默认禁止环回、链路本地和私网目标，降低 SSRF 风险。
- 如未来需要私网 Webhook，另行设计显式开关，不在本次范围内。
- 超时、重试和白名单沿用现有系统设置保存和重启生效机制。
- 环境路由通过专用管理 API 更新；保存成功后刷新不可变内存快照，无需重启 ccLoad。
- 每个模型请求从内存快照按环境标识查找，不在热路径查询数据库。

## 9. 身份传播与数据模型

鉴权成功后，请求上下文保存：

```text
external_user_id
ccload_token_hash
ccload_auth_token_id
authz_token_exp
```

持久化变更：

- `logs` 增加可空 `external_user_id`。
- SQLite、MySQL、PostgreSQL 均增加兼容迁移。
- Redis `UsageEvent` 增加可选 `external_user_id`。
- 外部鉴权关闭或迁移白名单绕过时，该字段为空。

归属规则：

- 外部平台用户关联和结算维度使用 `external_user_id`。
- ccLoad 模型、渠道、额度、并发、Token 统计继续使用返回的 ccLoad Token。
- 日志和 Redis 事件同时保留 `external_user_id` 与 `auth_token_id`，支持跨系统关联与审计。
- `authz_token_exp` 只存在请求内存中，不落库。

## 10. 可观测性与安全

新增指标：

```text
external_auth_requests_total
external_auth_allowed_total
external_auth_denied_total
external_auth_errors_total
external_auth_retries_total
external_auth_bypassed_total
external_auth_duration
```

日志可以记录：

- 请求 ID。
- 结果类别。
- Webhook 状态码。
- 总耗时。
- 重试次数。
- 白名单绕过事件。

日志禁止记录：

- 用户原始 Authorization。
- `X-Original-Authorization`。
- `X-Ccload-Token`。
- Webhook 响应正文。

网络安全：

- Webhook 必须使用 HTTPS。
- 外部平台使用 ccLoad 服务器固定出口 IP 白名单识别调用方。
- HTTP 客户端禁止重定向。
- 设置 URL 进行 SSRF 防护和 DNS/IP 校验。
- 外部平台不得依据未经可信代理验证的 `X-Forwarded-For` 判断 ccLoad 来源。

## 11. 错误响应

客户端只接收稳定、通用的错误：

- 缺少 Authorization：`401 Unauthorized`
- 外部平台明确拒绝：`403 Forbidden`
- 外部鉴权不可用、响应非法或返回无效 ccLoad Token：`503 Service Unavailable`
- ccLoad Token通过外部鉴权后触发本地模型、渠道、额度或并发限制：保持现有对应状态码和响应格式

不得向客户端暴露 Webhook URL、响应正文、内部状态或 Token。

## 12. 测试与验收

### 12.1 配置

- 外部鉴权默认关闭。
- 开启时至少有一个启用环境，且所有启用环境 URL 都是允许的 HTTPS 目标。
- 环境标识唯一并符合格式；重复、空值、大写和非法字符拒绝保存。
- 环境配置新增、编辑、启停和删除后刷新运行时快照。
- 超时和重试范围校验。
- 单 IP、CIDR、多个条目和非法白名单校验。
- SSRF 目标被拒绝。

### 12.2 HTTP/SSE

- 关闭外部鉴权时现有 Bearer、X-API-Key、Google Key 和 query key 行为不变。
- 开启后非白名单请求缺少 Authorization 返回 401。
- 缺少、空白、重复、非法、未知或已停用的 `X-Sedna-Env` Fail-closed。
- 合法 `X-Sedna-Env` 选择对应的 authz URL。
- ccLoad 只向 authz 发送规范环境标识，不盲目透传客户端值。
- 环境不合法时不调用 authz、不选择渠道、不请求上游。
- 客户端伪造 `X-Original-Authorization` 被覆盖。
- Webhook 收到当前请求的原始 Authorization 和正确元数据。
- 成功响应三个 Header 均被解析。
- 用户 UUID、过期时间或 ccLoad Token缺失、非法时返回 503。
- 返回的 ccLoad Token不存在、禁用或过期时返回 503。
- 外部用户 UUID 正确进入日志和 Redis 事件。

### 12.3 重试与失败

- 401/403 不重试并返回 403。
- 其他确定性 4xx 不重试并返回 503。
- 408、429、5xx、超时和网络错误最多重试 2 次。
- 重试后成功可以继续请求。
- 重试全部失败返回 503。
- 鉴权失败时没有上游请求、并发槽或用量费用。

### 12.4 白名单

- 命中可信来源 CIDR 时不调用 Webhook。
- 绕过后仍执行原有 ccLoad Token鉴权。
- 未命中时必须外部鉴权。
- 伪造 Host、`X-Forwarded-For` 或鉴权 Header 不能绕过。

### 12.5 WebSocket

- 握手缺少 Authorization 时拒绝。
- 每个 `response.create` 都重新调用 Webhook。
- 同连接连续请求可以获得不同用户和 ccLoad Token身份。
- 当前请求的日志和用量归属最新鉴权结果。
- 鉴权拒绝不关闭整个连接，返回当前消息对应的协议错误；客户端可在后续消息重新鉴权。
- 外部鉴权临时故障按相同重试策略处理。

### 12.6 敏感数据

- 原始 Authorization 和返回的 ccLoad Token不出现在普通日志。
- 不出现在调试日志、错误响应、数据库和 Redis。
- 外部用户 UUID可以按设计进入日志和 Redis。

### 12.7 回归

- 运行 `go test -tags sonic ./internal/...`。
- 运行完整 Go 构建。
- 运行相关前端设置页面测试。
- 验证 SQLite、MySQL、PostgreSQL 迁移。
- 验证服务优雅关闭不会遗留鉴权请求。

## 13. 部署顺序

1. 部署包含新设置但默认关闭的 ccLoad。
2. 在管理后台配置并验证 develop/test 环境的 authz HTTPS URL 与出口 IP 白名单。
3. 配置迁移来源 CIDR。
4. 开启外部鉴权并重启。
5. 观察允许、拒绝、错误、重试和绕过指标。
6. 逐步迁移旧客户端到外部平台 Authorization。
7. 清空 `external_auth_bypass_cidrs`，完成强制外部鉴权。

## 14. 完成标准

- 所有模型请求按配置执行外部鉴权或可信白名单绕过。
- WebSocket 每个 `response.create` 独立鉴权。
- Webhook 返回的 ccLoad Token完整复用现有权限、额度、并发和统计链路。
- `external_user_id` 正确写入日志和 Redis 用量事件。
- 任意鉴权服务异常均 Fail-closed。
- 敏感 Token不被持久化或泄漏。
- 配置、迁移和回归测试全部通过。
