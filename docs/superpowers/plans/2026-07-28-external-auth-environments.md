# 外部鉴权环境路由实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 让客户端通过唯一的 `X-Sedna-Env` 选择 ccLoad 管理后台中已启用的环境配置，并由 ccLoad 调用该环境对应的 authz URL。

**架构：** 新建 `external_auth_environments` 表和专用管理 API，运行时将启用配置发布为不可变快照。`ExternalAuthService` 不再持有单一 Webhook URL，而是先规范化并查找请求环境，再用匹配配置的 URL 和环境名调用 authz；未知、停用或歧义环境在任何网络调用前 Fail-closed。

**技术栈：** Go、Gin、`net/http`、SQLite/MySQL/PostgreSQL、项目现有 Store/HybridStore、原生 JavaScript 管理页面与 Go/JS 测试。

---

## 文件结构

- 创建 `internal/model/external_auth_environment.go`：环境配置持久化模型和环境名规范化。
- 创建 `internal/storage/sql/external_auth_environments.go`：三数据库通用 CRUD。
- 创建 `internal/storage/sql/external_auth_environments_test.go`：SQLite CRUD、唯一性和启停测试。
- 修改 `internal/storage/schema/tables.go`、`internal/storage/migrate.go` 及三数据库迁移测试：创建环境表、唯一索引和启用索引。
- 修改 `internal/storage/store.go`、`internal/storage/hybrid_store.go`：暴露环境 CRUD。
- 创建 `internal/app/admin_external_auth_environments.go` 及测试：管理员 CRUD、校验和运行时快照刷新。
- 修改 `internal/app/external_auth.go` 及测试：按 `X-Sedna-Env` 选择 URL并设置出站 Header。
- 修改 `internal/app/server.go` 及路由测试：挂载管理 API，并让外部鉴权中间件读取请求环境。
- 修改 `web/settings.html`、`web/assets/js/settings.js`、中英文语言包及前端测试：提供环境列表编辑器。

### 任务 1：定义环境模型、表和存储 CRUD

**文件：**
- 创建：`internal/model/external_auth_environment.go`
- 创建：`internal/storage/sql/external_auth_environments.go`
- 创建：`internal/storage/sql/external_auth_environments_test.go`
- 修改：`internal/storage/schema/tables.go`
- 修改：`internal/storage/migrate.go`
- 修改：`internal/storage/store.go`
- 修改：`internal/storage/hybrid_store.go`
- 测试：`internal/storage/migrate_sqlite_test.go`
- 测试：`internal/storage/migrate_mysql_test.go`
- 测试：`internal/storage/migrate_postgres_test.go`

- [x] **步骤 1：编写失败测试**

```go
func TestExternalAuthEnvironmentCRUD(t *testing.T) {
	store := newTestStore(t, "external_auth_environments.db")
	created, err := store.CreateExternalAuthEnvironment(context.Background(), &model.ExternalAuthEnvironment{
		Environment: "develop",
		AuthzURL:    "https://sedna-dev.example.com/internal/llm/authz",
		IsActive:   true,
	})
	require.NoError(t, err)
	require.NotZero(t, created.ID)

	got, err := store.ListExternalAuthEnvironments(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "develop", got[0].Environment)

	_, err = store.CreateExternalAuthEnvironment(context.Background(), &model.ExternalAuthEnvironment{
		Environment: "develop",
		AuthzURL:    "https://other.example.com/internal/llm/authz",
		IsActive:   true,
	})
	require.ErrorIs(t, err, model.ErrExternalAuthEnvironmentConflict)
}
```

迁移测试同时断言 `external_auth_environments` 存在，`environment` 唯一，字段包含 `id/environment/authz_url/is_active/created_at/updated_at`。

- [x] **步骤 2：运行测试验证失败**

运行：

```bash
rtk go test -tags sonic ./internal/storage/... -run 'ExternalAuthEnvironment|ExternalAuthEnvironments'
```

预期：FAIL，模型、表和 Store 方法尚未定义。

- [x] **步骤 3：编写最少实现**

```go
var ErrExternalAuthEnvironmentConflict = errors.New("external auth environment already exists")

type ExternalAuthEnvironment struct {
	ID          int64  `json:"id"`
	Environment string `json:"environment"`
	AuthzURL    string `json:"authz_url"`
	IsActive    bool   `json:"is_active"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

func NormalizeExternalAuthEnvironment(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" || !externalAuthEnvironmentPattern.MatchString(value) {
		return "", ErrInvalidExternalAuthEnvironment
	}
	return value, nil
}
```

表结构：

```go
func DefineExternalAuthEnvironmentsTable() *TableBuilder {
	return NewTable("external_auth_environments").
		Column("id INT PRIMARY KEY AUTO_INCREMENT").
		Column("environment VARCHAR(64) NOT NULL UNIQUE").
		Column("authz_url VARCHAR(2048) NOT NULL").
		Column("is_active TINYINT NOT NULL DEFAULT 1").
		Column("created_at BIGINT NOT NULL").
		Column("updated_at BIGINT NOT NULL").
		Index("idx_external_auth_environments_active", "is_active")
}
```

CRUD 使用现有方言 rebind 和显式 ID 辅助逻辑；删除不存在返回 `model.ErrExternalAuthEnvironmentNotFound`，唯一约束统一映射为 conflict。

- [x] **步骤 4：运行测试验证通过**

运行：

```bash
rtk go test -tags sonic ./internal/storage/... -run 'ExternalAuthEnvironment|ExternalAuthEnvironments'
```

预期：PASS。

- [x] **步骤 5：Commit**

```bash
rtk git add internal/model/external_auth_environment.go internal/storage
rtk git commit -m "feat: persist external auth environments"
```

### 任务 2：按请求环境选择 authz URL

**文件：**
- 修改：`internal/app/external_auth.go`
- 修改：`internal/app/external_auth_test.go`

- [x] **步骤 1：编写失败测试**

```go
func TestExternalAuthAuthorizeSelectsConfiguredEnvironment(t *testing.T) {
	develop := newExternalAuthTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "develop", r.Header.Get("X-Sedna-Env"))
		writeValidExternalAuthResponse(w)
	})
	service := newTestExternalAuthServiceWithEnvironments(t, map[string]string{
		"develop": develop.URL,
	})

	_, err := service.Authorize(context.Background(), externalAuthRequest{
		Environment:           "develop",
		OriginalAuthorization: "Bearer platform-jwt",
	})
	require.NoError(t, err)
}

func TestExternalAuthAuthorizeRejectsUnknownEnvironmentBeforeNetwork(t *testing.T) {
	service := newTestExternalAuthServiceWithEnvironments(t, nil)
	_, err := service.Authorize(context.Background(), externalAuthRequest{Environment: "test"})
	require.Error(t, err)
	assert.True(t, isExternalAuthErrorKind(err, externalAuthErrorDenied))
}
```

增加缺失、空白、重复 Header解析、非法大写、停用环境以及 develop/test 分别选择不同 URL 的测试。

- [x] **步骤 2：运行测试验证失败**

运行：

```bash
rtk go test -tags sonic ./internal/app -run 'ExternalAuth.*Environment'
```

预期：FAIL，`externalAuthRequest` 没有 `Environment`，服务仍使用单一 `WebhookURL`。

- [x] **步骤 3：编写最少实现**

```go
type externalAuthEnvironmentTarget struct {
	Environment string
	AuthzURL    *url.URL
}

type externalAuthConfig struct {
	Enabled        bool
	Environments   map[string]externalAuthEnvironmentTarget
	Timeout        time.Duration
	MaxRetries     int
	BypassPrefixes []netip.Prefix
}

type externalAuthRequest struct {
	Environment           string `json:"-"`
	OriginalAuthorization string `json:"-"`
	// existing metadata fields...
}
```

`Authorize` 在序列化和增加网络调用指标前查找启用环境；`authorizeAttempt` 接收选中的 target，并设置：

```go
req.Header.Set("X-Sedna-Env", target.Environment)
req.Header.Set("X-Original-Authorization", originalAuthorization)
```

未知、缺失、非法和停用环境统一返回 denied，不在错误中包含已配置环境列表或 URL。

- [x] **步骤 4：运行测试验证通过**

运行：

```bash
rtk go test -tags sonic ./internal/app -run 'ExternalAuth.*Environment|ExternalAuthAuthorize'
```

预期：PASS。

- [x] **步骤 5：Commit**

```bash
rtk git add internal/app/external_auth.go internal/app/external_auth_test.go
rtk git commit -m "feat: route external auth by environment"
```

### 任务 3：增加环境管理 API 和运行时快照

**文件：**
- 创建：`internal/app/admin_external_auth_environments.go`
- 创建：`internal/app/admin_external_auth_environments_test.go`
- 修改：`internal/app/server.go`
- 修改：`internal/app/server_test.go`

- [x] **步骤 1：编写失败测试**

```go
func TestAdminExternalAuthEnvironmentCRUD(t *testing.T) {
	env := newAdminExternalAuthTestServer(t)
	create := env.request(http.MethodPost, "/admin/external-auth/environments", `{
		"environment":"develop",
		"authz_url":"https://sedna-dev.example.com/internal/llm/authz",
		"is_active":true
	}`)
	require.Equal(t, http.StatusCreated, create.Code)

	duplicate := env.request(http.MethodPost, "/admin/external-auth/environments", `{
		"environment":"develop",
		"authz_url":"https://other.example.com/internal/llm/authz",
		"is_active":true
	}`)
	require.Equal(t, http.StatusConflict, duplicate.Code)
}
```

增加 GET、PUT、DELETE、404、非法环境名、HTTP URL、私网/环回 SSRF 地址、保存后快照立即更新，以及启用总开关但没有活动环境时拒绝的测试。

- [x] **步骤 2：运行测试验证失败**

运行：

```bash
rtk go test -tags sonic ./internal/app -run 'AdminExternalAuthEnvironment|ExternalAuthEnvironmentRoutes'
```

预期：FAIL，handler 和路由尚不存在。

- [x] **步骤 3：编写最少实现**

管理路由：

```go
admin.GET("/external-auth/environments", s.AdminListExternalAuthEnvironments)
admin.POST("/external-auth/environments", s.AdminCreateExternalAuthEnvironment)
admin.PUT("/external-auth/environments/:id", s.AdminUpdateExternalAuthEnvironment)
admin.DELETE("/external-auth/environments/:id", s.AdminDeleteExternalAuthEnvironment)
```

创建和更新先调用 `NormalizeExternalAuthEnvironment`，再复用 `validateExternalAuthEndpoint` 做 HTTPS/SSRF 校验。写入成功后从 Store 重载全部启用环境，并通过原子指针发布新的只读 map；读请求永远只读取完整快照。

- [x] **步骤 4：运行测试验证通过**

运行：

```bash
rtk go test -tags sonic ./internal/app -run 'AdminExternalAuthEnvironment|ExternalAuthEnvironmentRoutes'
```

预期：PASS。

- [x] **步骤 5：Commit**

```bash
rtk git add internal/app/admin_external_auth_environments.go internal/app/admin_external_auth_environments_test.go internal/app/server.go internal/app/server_test.go
rtk git commit -m "feat: manage external auth environments"
```

### 任务 4：在管理后台编辑环境配置

**文件：**
- 修改：`web/settings.html`
- 修改：`web/assets/js/settings.js`
- 修改：`web/assets/locales/zh-CN.js`
- 修改：`web/assets/locales/en.js`
- 修改或创建：`web/assets/js/settings.test.js`

- [x] **步骤 1：编写失败测试**

```javascript
test('external auth environment editor serializes enabled environment rows', () => {
  document.body.innerHTML = externalAuthEnvironmentFixture();
  addExternalAuthEnvironmentRow({
    environment: 'develop',
    authz_url: 'https://sedna-dev.example.com/internal/llm/authz',
    is_active: true
  });
  expect(readExternalAuthEnvironmentRows()).toEqual([{
    environment: 'develop',
    authz_url: 'https://sedna-dev.example.com/internal/llm/authz',
    is_active: true
  }]);
});
```

增加加载列表、新增、编辑、启停、删除确认、行内错误和服务端失败保留用户输入的测试。

- [x] **步骤 2：运行测试验证失败**

运行：

```bash
rtk npm test -- --run settings
```

预期：FAIL，环境编辑器函数和 DOM 不存在。

- [x] **步骤 3：编写最少实现**

设置页“外部请求鉴权”分组下新增环境表格，列为环境、Authz URL、启用、操作。保存使用专用 CRUD API；环境输入设置 `pattern="[a-z0-9_-]+"`，URL 输入使用 `type="url"`。删除必须二次确认，后端错误通过现有 `showError` 显示。

- [x] **步骤 4：运行测试验证通过**

运行：

```bash
rtk npm test -- --run settings
```

预期：PASS。

- [x] **步骤 5：Commit**

```bash
rtk git add web/settings.html web/assets/js/settings.js web/assets/js/settings.test.js web/assets/locales/zh-CN.js web/assets/locales/en.js
rtk git commit -m "feat: edit external auth environments"
```

### 任务 5：接入代理请求并执行回归验证

**文件：**
- 修改：`internal/app/server.go`
- 修改：`internal/app/auth_service.go`
- 修改：`internal/app/proxy_responses_websocket.go`
- 修改相应的 Go 测试和原外部鉴权实现计划完成标记。

- [ ] **步骤 1：完成原计划 HTTP/SSE 与 WebSocket 接入时的环境测试**

HTTP/SSE 请求从 Gin 原始 Header 使用 `c.Request.Header.Values("X-Sedna-Env")`，要求恰好一份；迁移 CIDR 绕过发生在环境要求之前。WebSocket 握手保存已规范化环境，每个 `response.create` 使用同一环境重新鉴权。

- [ ] **步骤 2：运行定向测试**

```bash
rtk go test -tags sonic ./internal/app -run 'ExternalAuth|ResponsesWebsocket.*Auth'
```

预期：PASS。

- [ ] **步骤 3：运行静态与全量验证**

```bash
rtk gofmt -w internal/app internal/model internal/storage
rtk go vet -tags sonic ./internal/...
rtk go test -tags sonic ./internal/...
rtk npm test
```

预期：全部通过且无诊断。

- [ ] **步骤 4：执行敏感数据回归搜索**

```bash
rtk rg -n 'X-Sedna-Env|X-Ccload-Token|X-Original-Authorization' internal web
```

预期：`X-Sedna-Env` 只进入规范化、路由、authz 出站请求和非敏感审计；JWT 与 ccLoad Token 不进入日志、数据库、Redis 或客户端响应。

- [ ] **步骤 5：Commit**

```bash
rtk git add internal web docs/superpowers/plans
rtk git commit -m "test: verify environment-routed external auth"
```
