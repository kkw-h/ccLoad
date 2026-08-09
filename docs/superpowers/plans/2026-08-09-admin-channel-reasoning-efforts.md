# 管理渠道推理强度响应实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 让渠道列表、详情和编辑器接口在每个模型条目中返回按原模型解析的 `supported_reasoning_efforts`。

**架构：** 保留 `model.ModelEntry` 作为纯持久化及请求结构；在管理响应层新增 `AdminChannelModelEntry`。`ChannelWithCooldown` 用直接声明的 `models` 响应字段覆盖嵌入 `model.Config` 的同名 JSON 字段，列表和详情通过同一个 Server 方法填充，编辑器继续复用详情结果。

**技术栈：** Go、Gin、`encoding/json`、现有 `modelReasoningCapabilityResolver`、Go table-driven tests。

---

## 文件结构

- 修改 `internal/app/admin_types.go`：定义管理响应模型条目，并让渠道响应显式持有 `models`。
- 修改 `internal/app/admin_channels.go`：新增共享转换函数，在列表与详情响应中填充推理强度。
- 修改 `internal/app/admin_crud_test.go`：验证列表和详情的已知、未知及显式空能力语义。
- 修改 `internal/app/admin_channels_more_test.go`：验证编辑器接口返回相同模型能力结构。

### 任务 1：锁定三个管理接口的响应契约

**文件：**
- 测试：`internal/app/admin_crud_test.go`
- 测试：`internal/app/admin_channels_more_test.go`

- [ ] **步骤 1：为列表和详情编写失败测试**

构造包含以下模型条目的渠道：

```go
[]model.ModelEntry{
    {Model: "sciland-3.0", RedirectModel: "gpt-5.6-sol"},
    {Model: "unknown-model"},
    {Model: "no-thinking", RedirectModel: "disabled-thinking"},
}
```

为测试 Server 注入覆盖配置：

```go
resolver, err := newModelReasoningCapabilityResolver(`{"disabled-thinking":[]}`)
if err != nil {
    t.Fatal(err)
}
server.modelReasoningCapabilities = resolver
```

断言 `sciland-3.0` 返回 `low/medium/high/xhigh`，未知模型省略字段，`no-thinking` 返回非 nil 空数组。

- [ ] **步骤 2：为编辑器编写失败测试**

请求 `GET /admin/channels/:id/editor`，断言 `data.channel.models` 与详情接口具有相同字段和值。

- [ ] **步骤 3：运行测试并确认因字段缺失而失败**

运行：

```bash
rtk go test -tags sonic ./internal/app -run 'TestHandle(ListChannels|GetChannel|ChannelEditor).*Reasoning' -count=1
```

预期：FAIL，响应模型条目的 `supported_reasoning_efforts` 为 nil 或不存在。

### 任务 2：实现管理响应转换

**文件：**
- 修改：`internal/app/admin_types.go`
- 修改：`internal/app/admin_channels.go`

- [ ] **步骤 1：增加响应专用类型**

```go
type AdminChannelModelEntry struct {
    Model                     string    `json:"model"`
    RedirectModel             string    `json:"redirect_model,omitempty"`
    Disabled                  bool      `json:"disabled,omitempty"`
    SupportedReasoningEfforts *[]string `json:"supported_reasoning_efforts,omitempty"`
}
```

并在 `ChannelWithCooldown` 中增加：

```go
Models []AdminChannelModelEntry `json:"models"`
```

- [ ] **步骤 2：实现共享转换函数**

在 `admin_channels.go` 增加 Server 方法。它复制持久化字段、把空 `redirect_model` 补成 `model`，再以非空 `redirect_model` 或 `model` 调用 `s.modelReasoningCapabilities.Resolve`。解析成功时复制切片并保留显式空数组；解析器为空或模型未知时保持 nil。

- [ ] **步骤 3：接入列表、详情和编辑器**

`channelEnrichmentContext` 增加 Server 引用并在 `enrichChannel` 填充 `Models`；`buildChannelDetail` 同样填充 `Models`。编辑器无需单独逻辑，因为它复用 `buildChannelDetail`。

- [ ] **步骤 4：运行定向测试**

运行：

```bash
rtk go test -tags sonic ./internal/app -run 'TestHandle(ListChannels|GetChannel|ChannelEditor).*Reasoning' -count=1
```

预期：PASS。

- [ ] **步骤 5：提交功能**

```bash
rtk git add internal/app/admin_types.go internal/app/admin_channels.go internal/app/admin_crud_test.go internal/app/admin_channels_more_test.go
rtk git commit -m "feat(admin): expose channel reasoning capabilities"
```

### 任务 3：回归验证

**文件：**
- 修改：`docs/superpowers/plans/2026-08-09-admin-channel-reasoning-efforts.md`

- [ ] **步骤 1：运行管理渠道相关测试**

```bash
rtk go test -tags sonic ./internal/app -run 'TestHandle(ListChannels|GetChannel|ChannelEditor)|TestXAIChannelResponsesExposeOnlySafeOAuthMetadata|TestCodexOAuth' -count=1
```

预期：PASS。

- [ ] **步骤 2：运行完整 internal/app 测试**

```bash
rtk go test -tags sonic ./internal/app -count=1
```

预期：PASS。

- [ ] **步骤 3：执行格式和静态检查**

```bash
rtk gofmt -w internal/app/admin_types.go internal/app/admin_channels.go internal/app/admin_crud_test.go internal/app/admin_channels_more_test.go
rtk git diff --check
rtk golangci-lint run ./internal/app/...
```

预期：无格式错误、空白错误或 lint 问题。

- [ ] **步骤 4：记录计划完成状态并提交**

将上述复选框按实际结果更新为完成，然后提交计划文件。

