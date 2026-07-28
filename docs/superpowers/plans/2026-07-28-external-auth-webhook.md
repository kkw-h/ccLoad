# 外部鉴权 Webhook 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 为所有对外模型请求增加可选、失败关闭的外部鉴权 Webhook，并把外部用户 UUID 贯穿请求日志和 Redis 用量事件。

**架构：** 新建独立的 `ExternalAuthService`，负责配置解析、IP/CIDR 迁移绕过、HTTPS/SSRF 安全拨号、重试和响应头校验；Gin 中间件只读取客户端原始 `Authorization`，将 Webhook 返回的 ccLoad Token 和外部用户身份写入私有上下文，再复用现有 `AuthService` 做本地令牌、模型、渠道、费用和并发校验。HTTP/SSE 每请求鉴权一次，Responses WebSocket 握手鉴权后在每个 `response.create` 前重新调用 Webhook；敏感令牌只存在内存，不进入日志、响应、数据库或 Redis。

**技术栈：** Go、Gin、`net/http`、`net/netip`、SQLite/MySQL/PostgreSQL、Redis Streams、项目现有 JavaScript 设置页与 Go/JS 测试工具链。

---

## 文件结构

- 创建 `internal/app/external_auth.go`：外部鉴权配置、SSRF 安全 HTTP 客户端、CIDR 绕过、重试、结果分类、上下文身份和指标。
- 创建 `internal/app/external_auth_test.go`：服务级成功、拒绝、重试、超时、过期、CIDR 与 SSRF 测试。
- 修改 `internal/app/auth_service.go`、`internal/app/auth_service_test.go`：允许现有 API 鉴权从私有上下文读取 Webhook 返回的 ccLoad Token，并暴露 WebSocket 单次身份校验入口。
- 修改 `internal/app/server.go`、`internal/app/server_test.go`：启动时加载外部鉴权配置并只挂载到公开代理路由。
- 修改 `internal/app/proxy_responses_websocket.go`、`internal/app/proxy_responses_websocket_test.go`：每个 `response.create` 重鉴权并按当次身份执行。
- 修改 `internal/app/proxy_util.go`、`internal/app/event_publisher.go` 及对应测试：请求上下文、日志和两类用量事件传递 `external_user_id`。
- 修改 `internal/model/log.go`、`internal/model/usage_event.go`：新增外部用户字段。
- 修改 `internal/storage/schema/tables.go`、`internal/storage/migrate.go`、`internal/storage/migrate_columns.go` 及迁移测试：三种数据库新增日志列和索引，新增五项设置。
- 修改 `internal/storage/sql/log.go` 及日志存储测试：读写 `external_user_id`。
- 修改 `internal/app/admin_settings.go` 及测试：校验 Webhook URL、超时、重试次数和 CIDR。
- 修改 `internal/app/admin_active_requests.go` 及测试：公开外部鉴权计数和耗时指标。
- 修改 `web/assets/js/settings.js`、`web/assets/locales/zh-CN.js`、`web/assets/locales/en.js` 及前端测试：增加“外部鉴权”设置分组与文案。

### 任务 1：定义外部鉴权配置与安全边界

**文件：**
- 创建：`internal/app/external_auth.go`
- 创建：`internal/app/external_auth_test.go`

- [ ] **步骤 1：编写失败测试，覆盖配置、CIDR 和 SSRF**

```go
func TestLoadExternalAuthConfig(t *testing.T) {
	cfg, err := parseExternalAuthConfig(true, "https://auth.example.com/check", 2000, 2, "203.0.113.7,2001:db8::/32")
	require.NoError(t, err)
	assert.Equal(t, 2*time.Second, cfg.Timeout)
	assert.Len(t, cfg.BypassPrefixes, 2)
}

func TestValidateExternalAuthEndpointRejectsUnsafeTargets(t *testing.T) {
	for _, raw := range []string{"http://auth.example.com/check", "https://127.0.0.1/check", "https://169.254.169.254/latest"} {
		t.Run(raw, func(t *testing.T) {
			require.Error(t, validateExternalAuthEndpoint(context.Background(), raw, net.DefaultResolver))
		})
	}
}
```

- [ ] **步骤 2：运行测试确认失败**

运行：`rtk go test -tags sonic ./internal/app -run 'TestLoadExternalAuthConfig|TestValidateExternalAuthEndpoint'`

预期：FAIL，提示 `parseExternalAuthConfig` 或 `validateExternalAuthEndpoint` 未定义。

- [ ] **步骤 3：实现最小配置模型和安全地址判定**

```go
type externalAuthConfig struct {
	Enabled        bool
	WebhookURL     *url.URL
	Timeout        time.Duration
	MaxRetries     int
	BypassPrefixes []netip.Prefix
}

func isUnsafeExternalAuthIP(ip netip.Addr) bool {
	return !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified()
}
```

解析单个 IP 时转换为 `/32` 或 `/128`；启用时 URL 必须是 HTTPS、不能含用户信息，且初次 DNS 解析的所有地址必须为公网地址。自定义 `DialContext` 在每次连接时重新解析并复验目标 IP，防止 DNS rebinding。

- [ ] **步骤 4：运行测试确认通过**

运行：`rtk go test -tags sonic ./internal/app -run 'TestLoadExternalAuthConfig|TestValidateExternalAuthEndpoint'`

预期：PASS。

- [ ] **步骤 5：Commit**

```bash
rtk git add internal/app/external_auth.go internal/app/external_auth_test.go
rtk git commit -m "feat: define external auth security boundary"
```

### 任务 2：实现 Webhook 调用、重试、返回头校验和指标

**文件：**
- 修改：`internal/app/external_auth.go`
- 修改：`internal/app/external_auth_test.go`

- [ ] **步骤 1：编写失败测试覆盖结果矩阵**

```go
func TestExternalAuthAuthorizeRetriesTransientFailure(t *testing.T) {
	var calls atomic.Int32
	srv := newExternalAuthTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer platform-jwt", r.Header.Get("X-Original-Authorization"))
		if calls.Add(1) < 3 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("X-User-Id", "d9428888-122b-11e1-b85c-61cd3cbb3210")
		w.Header().Set("X-Ccload-Token", "local-secret")
		w.Header().Set("X-Authz-Token-Exp", strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10))
		w.WriteHeader(http.StatusNoContent)
	})
	result, err := newTestExternalAuthService(t, srv.URL).Authorize(context.Background(), externalAuthRequest{
		OriginalAuthorization: "Bearer platform-jwt",
		Method: "POST", Path: "/v1/responses", Model: "gpt-5", Stream: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "local-secret", result.CCLoadToken)
	assert.Equal(t, int32(3), calls.Load())
}
```

同时增加表驱动测试：401/403 为拒绝且不重试；408/429/5xx、网络错误和超时最多重试 2 次；其他 4xx 与缺失/非法响应头为不可用且不重试；`exp <= now+5s` 为不可用；请求 JSON 不包含 prompt；指标分别累计 requests/allowed/denied/errors/retries/duration。

- [ ] **步骤 2：运行测试确认失败**

运行：`rtk go test -tags sonic ./internal/app -run '^TestExternalAuthAuthorize'`

预期：FAIL，提示 `Authorize`、请求/结果类型或指标快照未定义。

- [ ] **步骤 3：实现调用协议与错误分类**

```go
type externalAuthRequest struct {
	RequestID             string `json:"request_id"`
	Method                string `json:"method"`
	Path                  string `json:"path"`
	Model                 string `json:"model,omitempty"`
	Stream                bool   `json:"stream"`
	ClientIP              string `json:"client_ip,omitempty"`
	OriginalAuthorization string `json:"-"`
}

type externalAuthResult struct {
	ExternalUserID string
	CCLoadToken    string
	ExpiresAt      time.Time
}
```

每次尝试使用独立的 `context.WithTimeout`；固定退避为 100ms、300ms，并注入 `jitter func(time.Duration) time.Duration` 使测试可设为零。只对网络错误、超时、408、429、5xx 重试。2xx 必须有合法 UUID、非空 ccLoad Token 和十进制 Unix 秒过期时间；敏感头不进入错误文本或日志。

- [ ] **步骤 4：运行测试确认通过**

运行：`rtk go test -tags sonic ./internal/app -run '^TestExternalAuthAuthorize'`

预期：PASS。

- [ ] **步骤 5：Commit**

```bash
rtk git add internal/app/external_auth.go internal/app/external_auth_test.go
rtk git commit -m "feat: call external authorization webhook"
```

### 任务 3：接入 HTTP/SSE 代理路由并复用本地令牌权限

**文件：**
- 修改：`internal/app/auth_service.go`
- 修改：`internal/app/auth_service_test.go`
- 修改：`internal/app/server.go`
- 修改：`internal/app/server_test.go`
- 修改：`internal/app/proxy_util.go`

- [ ] **步骤 1：编写失败测试覆盖中间件顺序和迁移绕过**

```go
func TestExternalAuthMiddlewareInjectsLocalTokenWithoutReplacingAuthorization(t *testing.T) {
	// Webhook 返回 local-secret；下游断言原 Authorization 仍是平台 JWT，
	// externalAuthIdentityFromContext 返回 UUID，RequireAPIAuth 解析的是 local-secret。
}

func TestExternalAuthMiddlewareRequiresAuthorizationWhenEnabled(t *testing.T) {
	// 无 Authorization 时返回 401，且不调用 Webhook。
}

func TestExternalAuthMiddlewareBypassesTrustedClientCIDR(t *testing.T) {
	// Gin ClientIP 命中白名单时不调用 Webhook，继续用请求中的 ccLoad Bearer Token。
}
```

再增加路由测试，确认 `/v1/*`、`/v1beta/*` 和 `/backend-api/codex/responses` 调用外部鉴权，而 `/admin`、`/dashboard`、`/health` 不调用。

- [ ] **步骤 2：运行测试确认失败**

运行：`rtk go test -tags sonic ./internal/app -run 'TestExternalAuthMiddleware|TestExternalAuthRoutes'`

预期：FAIL，现有路由未挂载中间件，`RequireAPIAuth` 只读取请求头。

- [ ] **步骤 3：实现私有上下文身份和可复用本地校验**

```go
type externalAuthIdentity struct {
	ExternalUserID string
	CCLoadToken    string
	ExpiresAt      time.Time
}

func (s *AuthService) resolveProxyAuthToken(c *gin.Context) (string, bool) {
	if identity, ok := externalAuthIdentityFromContext(c); ok {
		return identity.CCLoadToken, true
	}
	return extractLegacyAPIAuthToken(c.Request)
}
```

将 `RequireAPIAuth` 的本地令牌解析、有效期、模型/渠道/费用和并发身份绑定提取为可复用方法；中间件顺序固定为 `captureClientRequestMetadata()`、`s.externalAuthService.Middleware()`、`s.authService.RequireAPIAuth()`。对 HTTP 请求只调用一次 Webhook，不改变 `Authorization`，不接受 `X-API-Key`、`x-goog-api-key` 或查询参数代替外部身份。

- [ ] **步骤 4：运行相关测试**

运行：`rtk go test -tags sonic ./internal/app -run 'TestExternalAuthMiddleware|TestExternalAuthRoutes|TestRequireAPIAuth'`

预期：PASS。

- [ ] **步骤 5：Commit**

```bash
rtk git add internal/app/auth_service.go internal/app/auth_service_test.go internal/app/server.go internal/app/server_test.go internal/app/proxy_util.go
rtk git commit -m "feat: enforce external auth on proxy routes"
```

### 任务 4：Responses WebSocket 每次创建响应前重鉴权

**文件：**
- 修改：`internal/app/proxy_responses_websocket.go`
- 修改：`internal/app/proxy_responses_websocket_test.go`
- 修改：`internal/app/auth_service.go`

- [ ] **步骤 1：编写失败测试**

```go
func TestResponsesWebsocketReauthorizesEveryResponseCreate(t *testing.T) {
	// 同一连接依次发送两个 response.create；Webhook 返回两个不同 UUID 和 ccLoad Token。
	// 断言调用次数含握手共 3 次，并且两个 turn 分别使用各自 tokenHash/tokenID。
}

func TestResponsesWebsocketAuthFailureKeepsConnectionOpen(t *testing.T) {
	// 第一次 response.create 被拒绝，收到 external_auth_denied；
	// 第二次鉴权成功并完成，连接未因第一次失败关闭。
}
```

- [ ] **步骤 2：运行测试确认失败**

运行：`rtk go test -tags sonic ./internal/app -run 'TestResponsesWebsocket.*Auth'`

预期：FAIL，当前连接只复用握手时的身份。

- [ ] **步骤 3：实现逐 turn 身份刷新**

在处理 `response.create` 后、获取 execution session 和上游渠道前，用握手请求中原始 `Authorization` 及当前消息的 model/stream 再调用 `Authorize`；再用 `AuthService` 的可复用入口校验返回的 ccLoad Token并获取当次 tokenHash/tokenID/environment。拒绝写 `external_auth_denied`，瞬态或无效本地令牌写 `external_auth_unavailable`，然后继续读取下一条消息；`response.append` 只延续其所属 create 的已认证执行会话，不额外鉴权。

- [ ] **步骤 4：运行测试确认通过**

运行：`rtk go test -tags sonic ./internal/app -run 'TestResponsesWebsocket.*Auth'`

预期：PASS。

- [ ] **步骤 5：Commit**

```bash
rtk git add internal/app/proxy_responses_websocket.go internal/app/proxy_responses_websocket_test.go internal/app/auth_service.go
rtk git commit -m "feat: reauthorize responses websocket turns"
```

### 任务 5：持久化外部用户 UUID 到日志

**文件：**
- 修改：`internal/model/log.go`
- 修改：`internal/storage/schema/tables.go`
- 修改：`internal/storage/migrate_columns.go`
- 修改：`internal/storage/migrate_sqlite_test.go`
- 修改：`internal/storage/migrate_postgres_test.go`
- 修改：`internal/storage/sql/log.go`
- 修改：`internal/storage/sql/log_test.go`
- 修改：`internal/app/proxy_util.go`
- 修改：`internal/app/proxy_util_test.go`

- [ ] **步骤 1：编写失败测试**

```go
func TestAddAndListLogPreservesExternalUserID(t *testing.T) {
	entry := &model.LogEntry{ExternalUserID: "d9428888-122b-11e1-b85c-61cd3cbb3210"}
	require.NoError(t, store.AddLog(context.Background(), entry))
	got, err := store.ListLogs(context.Background(), time.Unix(0, 0), 10, 0, nil)
	require.NoError(t, err)
	assert.Equal(t, entry.ExternalUserID, got[0].ExternalUserID)
}
```

增加 SQLite/PostgreSQL 旧表迁移测试，断言 `external_user_id` 列和 `idx_logs_external_user_time` 索引存在；MySQL 迁移用现有 sqlmock 风格断言 `ALTER TABLE`/`CREATE INDEX`。

- [ ] **步骤 2：运行测试确认失败**

运行：`rtk go test -tags sonic ./internal/storage/... ./internal/app -run 'ExternalUserID|ExternalUser'`

预期：FAIL，模型和数据库列尚不存在。

- [ ] **步骤 3：实现模型、DDL、迁移和 SQL 映射**

```go
type LogEntry struct {
	// existing fields...
	ExternalUserID string `json:"external_user_id,omitempty"`
}
```

新库列使用 `VARCHAR(36) NOT NULL DEFAULT ''`，SQLite 使用 `TEXT NOT NULL DEFAULT ''`；索引顺序为 `(external_user_id, time)`。同步更新所有日志 SELECT 列、`scanner.Scan`、INSERT 列、占位符、`logRowParams` 和 `logRowArgs`，并由 `proxyRequestContext`、`logEntryParams` 传入构建函数。

- [ ] **步骤 4：运行测试确认通过**

运行：`rtk go test -tags sonic ./internal/storage/... ./internal/app -run 'ExternalUserID|ExternalUser'`

预期：PASS。

- [ ] **步骤 5：Commit**

```bash
rtk git add internal/model/log.go internal/storage/schema/tables.go internal/storage/migrate_columns.go internal/storage/migrate_*_test.go internal/storage/sql/log.go internal/storage/sql/log_test.go internal/app/proxy_util.go internal/app/proxy_util_test.go
rtk git commit -m "feat: persist external user on proxy logs"
```

### 任务 6：把外部用户写入 Redis 用量事件

**文件：**
- 修改：`internal/model/usage_event.go`
- 修改：`internal/app/event_publisher.go`
- 修改：`internal/app/event_publisher_test.go`
- 修改：`internal/app/proxy_util.go`
- 修改：`internal/app/proxy_util_test.go`
- 修改：`internal/eventbus/redis_stream_test.go`

- [ ] **步骤 1：编写失败测试**

```go
func TestUsageEventsIncludeExternalUserIDWithoutSecrets(t *testing.T) {
	event := buildAttemptUsageEvent(logEntryParams{
		ExternalUserID: "d9428888-122b-11e1-b85c-61cd3cbb3210",
	}, &model.LogEntry{})
	raw, err := json.Marshal(event)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"external_user_id":"d9428888-122b-11e1-b85c-61cd3cbb3210"`)
	assert.NotContains(t, string(raw), "local-secret")
	assert.NotContains(t, string(raw), "platform-jwt")
}
```

- [ ] **步骤 2：运行测试确认失败**

运行：`rtk go test -tags sonic ./internal/app ./internal/eventbus -run 'UsageEventsIncludeExternalUser|UsageEvent'`

预期：FAIL，`UsageEvent` 没有 `ExternalUserID`。

- [ ] **步骤 3：实现 attempt/request 两类事件字段**

```go
type UsageEvent struct {
	// existing fields...
	ExternalUserID string `json:"external_user_id,omitempty"`
}
```

`buildAttemptUsageEvent` 和 `publishRequestUsageEvent` 均从请求上下文复制 UUID；不增加 Webhook Token、平台 Authorization 或 JWT 过期时间字段。

- [ ] **步骤 4：运行测试确认通过**

运行：`rtk go test -tags sonic ./internal/app ./internal/eventbus -run 'UsageEventsIncludeExternalUser|UsageEvent'`

预期：PASS。

- [ ] **步骤 5：Commit**

```bash
rtk git add internal/model/usage_event.go internal/app/event_publisher.go internal/app/event_publisher_test.go internal/app/proxy_util.go internal/app/proxy_util_test.go internal/eventbus/redis_stream_test.go
rtk git commit -m "feat: publish external user in usage events"
```

### 任务 7：增加设置、校验和管理界面

**文件：**
- 修改：`internal/storage/migrate.go`
- 修改：`internal/storage/migrate_sqlite_test.go`
- 修改：`internal/app/admin_settings.go`
- 修改：`internal/app/admin_settings_test.go`
- 修改：`internal/app/server.go`
- 修改：`web/assets/js/settings.js`
- 修改：`web/assets/locales/zh-CN.js`
- 修改：`web/assets/locales/en.js`
- 测试：`web/assets/js/settings.test.js`

- [ ] **步骤 1：编写失败测试**

```go
func TestValidateExternalAuthSettings(t *testing.T) {
	require.NoError(t, validateSettingValue("external_auth_webhook_url", "string", "https://auth.example.com/check"))
	require.Error(t, validateSettingValue("external_auth_webhook_url", "string", "http://auth.example.com/check"))
	require.Error(t, validateSettingValue("external_auth_timeout_ms", "int", "0"))
	require.Error(t, validateSettingValue("external_auth_max_retries", "int", "3"))
	require.Error(t, validateSettingValue("external_auth_bypass_cidrs", "string", "not-a-cidr"))
}
```

迁移测试断言默认值：enabled=false、URL 空、timeout=2000、max_retries=2、bypass_cidrs 空；前端测试断言五个键归入独立 `external-auth` 分组。

- [ ] **步骤 2：运行测试确认失败**

运行：`rtk go test -tags sonic ./internal/app ./internal/storage -run 'ExternalAuthSettings|DefaultSettings'`

运行：`rtk npm test -- --run settings`

预期：Go 或 JS 测试 FAIL，设置尚未定义。

- [ ] **步骤 3：实现设置和启动加载**

```go
{"external_auth_enabled", "false", "bool", "启用外部鉴权 Webhook", "false"},
{"external_auth_webhook_url", "", "string", "外部鉴权 HTTPS URL", ""},
{"external_auth_timeout_ms", "2000", "int", "外部鉴权单次超时(毫秒)", "2000"},
{"external_auth_max_retries", "2", "int", "外部鉴权最大重试次数(0-2)", "2"},
{"external_auth_bypass_cidrs", "", "string", "迁移期跳过外部鉴权的客户端 IP/CIDR", ""},
```

超时限制 100–10000ms，重试限制 0–2；URL 空仅在 disabled 时允许，非空必须 HTTPS；CIDR 逐项解析。`NewServer` 从 `ConfigService` 加载一次并创建服务，配置保存后沿用现有重启机制。前端分组顺序放在“访问控制”前，并补齐中英文名称。

- [ ] **步骤 4：运行测试确认通过**

运行：`rtk go test -tags sonic ./internal/app ./internal/storage -run 'ExternalAuthSettings|DefaultSettings'`

运行：`rtk npm test -- --run settings`

预期：PASS。

- [ ] **步骤 5：Commit**

```bash
rtk git add internal/storage/migrate.go internal/storage/migrate_sqlite_test.go internal/app/admin_settings.go internal/app/admin_settings_test.go internal/app/server.go web/assets/js/settings.js web/assets/js/settings.test.js web/assets/locales/zh-CN.js web/assets/locales/en.js
rtk git commit -m "feat: add external auth settings"
```

### 任务 8：公开运行时指标并验证脱敏

**文件：**
- 修改：`internal/app/admin_active_requests.go`
- 修改：`internal/app/admin_active_requests_debug_test.go`
- 修改：`internal/app/external_auth_test.go`
- 修改：`internal/app/debug_capture_test.go`

- [ ] **步骤 1：编写失败测试**

```go
func TestHandleRuntimeMetricsExposesExternalAuth(t *testing.T) {
	srv.externalAuthService.metrics.allowed.Add(2)
	srv.HandleRuntimeMetrics(c)
	assert.JSONEq(t, `{"data":{"responses_websocket":{},"external_auth":{"requests_total":2,"allowed":2,"denied":0,"errors":0,"retries":0,"bypassed":0}}}`, recorder.Body.String())
}
```

增加 debug capture 测试，向请求写入平台 Authorization 和 Webhook 返回 token，断言调试日志、普通日志、错误响应均不包含二者。

- [ ] **步骤 2：运行测试确认失败**

运行：`rtk go test -tags sonic ./internal/app -run 'RuntimeMetricsExposesExternalAuth|ExternalAuthSecrets'`

预期：FAIL，指标尚未加入响应或敏感头仍会被捕获。

- [ ] **步骤 3：实现指标快照和脱敏规则**

运行时响应新增：

```json
{
  "external_auth": {
    "requests_total": 0,
    "allowed": 0,
    "denied": 0,
    "errors": 0,
    "retries": 0,
    "bypassed": 0,
    "duration_ms_total": 0
  }
}
```

在统一请求头脱敏函数中加入 `X-Original-Authorization`、`X-Ccload-Token`；响应头捕获也过滤这两项和 `X-Authz-Token-Exp`。Webhook 错误只记录状态分类、attempt 和 request_id。

- [ ] **步骤 4：运行测试确认通过**

运行：`rtk go test -tags sonic ./internal/app -run 'RuntimeMetricsExposesExternalAuth|ExternalAuthSecrets'`

预期：PASS。

- [ ] **步骤 5：Commit**

```bash
rtk git add internal/app/admin_active_requests.go internal/app/admin_active_requests_debug_test.go internal/app/external_auth_test.go internal/app/debug_capture_test.go
rtk git commit -m "feat: expose external auth metrics safely"
```

### 任务 9：全量验证与文档一致性检查

**文件：**
- 修改：`docs/superpowers/specs/2026-07-28-external-auth-webhook-design.md`（仅在实现揭示必要澄清时）
- 修改：`docs/superpowers/plans/2026-07-28-external-auth-webhook.md`（勾选已完成步骤）

- [ ] **步骤 1：格式化和静态检查**

运行：`rtk gofmt -w internal/app internal/model internal/storage internal/eventbus`

运行：`rtk go vet -tags sonic ./internal/...`

预期：命令成功，无诊断。

- [ ] **步骤 2：运行全量 Go 测试**

运行：`rtk go test -tags sonic ./internal/...`

预期：全部通过；若已知探测冷却测试偶发超时，单独以 `-count=3` 复验并记录证据，不忽略其他失败。

- [ ] **步骤 3：运行前端测试**

运行：`rtk npm test`

预期：全部通过。

- [ ] **步骤 4：执行安全回归搜索**

运行：`rtk rg -n 'X-Ccload-Token|X-Original-Authorization|CCLoadToken|OriginalAuthorization' internal | sort`

预期：命中仅限外部鉴权调用、上下文和明确的脱敏测试；日志模型、UsageEvent、Redis payload 构造和客户端响应代码没有敏感字段。

- [ ] **步骤 5：执行规格覆盖检查**

逐项核对设计文档中的：fail-closed、IP 白名单、三次尝试、2 秒超时、状态码分类、5 秒过期余量、无缓存、每个 `response.create`、本地令牌权限复用、三数据库迁移、Redis UUID、指标和设置页。所有条目必须有自动化测试。

- [ ] **步骤 6：最终 Commit**

```bash
rtk git add internal web docs/superpowers
rtk git commit -m "test: verify external auth integration"
```
