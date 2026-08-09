# 模型推理强度发现实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 根据渠道模型的 `redirect_model` 为 `/v1/models` 返回 `supported_reasoning_efforts`，并允许管理员按原模型名称进行全局人工覆盖。

**架构：** 新建线程安全的推理能力解析器，内置 `gpt-5.6-sol` 能力并用系统设置 JSON 覆盖。模型列表处理器从当前 Token 可见的渠道条目计算安全交集；设置页使用独立的纯 JavaScript 编辑器维护覆盖映射。只有这项设置发生变化时热更新解析器并跳过重启，混合设置更新仍沿用现有重启行为。

**技术栈：** Go 1.25、Gin、sonic JSON、PostgreSQL/SQLite/MySQL 系统设置迁移、原生 JavaScript、Node `node:test`。

---

## 文件结构

- 创建 `internal/app/model_reasoning_capabilities.go`：规范化强度、解析人工覆盖、线程安全解析和多渠道交集。
- 创建 `internal/app/model_reasoning_capabilities_test.go`：解析器、覆盖优先级和交集单元测试。
- 修改 `internal/app/server.go`：Server 持有解析器并在启动时从 ConfigService 初始化。
- 修改 `internal/app/proxy_gemini.go`：从可见渠道构建模型能力并扩展 OpenAI/Codex/Anthropic 返回对象。
- 修改 `internal/app/proxy_gemini_test.go`：模型列表能力字段和权限隔离回归测试。
- 修改 `internal/storage/migrate.go`：新增 `model_reasoning_effort_overrides` 系统设置。
- 修改 `internal/storage/migrate_sqlite_test.go`、`internal/storage/migrate_postgres_test.go`：验证新设置跨方言迁移元数据。
- 修改 `internal/app/admin_settings.go`：JSON 校验、热更新和条件重启。
- 修改 `internal/app/admin_settings_handler_test.go`：设置验证、热更新及重启策略测试。
- 创建 `web/assets/js/model-reasoning-efforts.js`：纯函数解析、规范化和编辑器状态管理。
- 创建 `web/assets/js/model-reasoning-efforts.test.js`：前端映射编辑器单元测试。
- 修改 `web/assets/js/settings.js`、`web/assets/js/settings.test.js`：设置页弹窗集成和保存序列化。
- 修改 `web/settings.html`、`web/assets/css/styles.css`：推理强度覆盖编辑弹窗及样式。
- 修改 `web/assets/locales/zh-CN.js`、`web/assets/locales/en.js`：中英文文案。
- 修改 `web/openapi.yaml`：记录新系统设置 JSON 语义和 `/v1/models` 扩展字段说明。

### 任务 1：实现纯后端能力解析器

**文件：**
- 创建：`internal/app/model_reasoning_capabilities.go`
- 创建：`internal/app/model_reasoning_capabilities_test.go`

- [ ] **步骤 1：编写失败的解析器测试**

测试固定顺序、内置能力、人工覆盖、空数组、未知模型和多原模型交集：

```go
func TestModelReasoningCapabilityResolver(t *testing.T) {
    resolver, err := newModelReasoningCapabilityResolver(`{
        "gpt-5.6-sol":["HIGH","low","high"],
        "no-reasoning":[]
    }`)
    if err != nil { t.Fatal(err) }

    got, known := resolver.Resolve("gpt-5.6-sol")
    assertEfforts(t, got, []string{"low", "high"}, known)
    got, known = resolver.Resolve(" no-reasoning ")
    assertEfforts(t, got, []string{}, known)
    _, known = resolver.Resolve("unknown-model")
    if known { t.Fatal("unknown model must stay unknown") }
}

func TestResolvePublicModelReasoningEfforts(t *testing.T) {
    resolver, _ := newModelReasoningCapabilityResolver(`{
        "upstream-a":["low","medium","high"],
        "upstream-b":["medium","high","xhigh"]
    }`)
    got, known := resolver.ResolveAll([]string{"upstream-a", "upstream-b"})
    assertEfforts(t, got, []string{"medium", "high"}, known)

    if _, known = resolver.ResolveAll([]string{"upstream-a", "unknown"}); known {
        t.Fatal("one unknown route must make public capability unknown")
    }
}
```

- [ ] **步骤 2：运行测试确认失败**

运行：

```bash
rtk go test -tags sonic ./internal/app -run 'TestModelReasoningCapabilityResolver|TestResolvePublicModelReasoningEfforts' -count=1
```

预期：编译失败，提示 `newModelReasoningCapabilityResolver` 未定义。

- [ ] **步骤 3：实现最小解析器**

实现固定枚举、内置表、JSON 校验和原子快照：

```go
const modelReasoningEffortOverridesSetting = "model_reasoning_effort_overrides"

var reasoningEffortOrder = []string{
    "none", "minimal", "low", "medium", "high", "xhigh", "max",
}

var builtInModelReasoningEfforts = map[string][]string{
    "gpt-5.6-sol": {"low", "medium", "high", "xhigh"},
}

type modelReasoningCapabilityResolver struct {
    overrides atomic.Pointer[map[string][]string]
}

func newModelReasoningCapabilityResolver(raw string) (*modelReasoningCapabilityResolver, error) {
    resolver := &modelReasoningCapabilityResolver{}
    if err := resolver.SetOverrides(raw); err != nil { return nil, err }
    return resolver, nil
}

func (r *modelReasoningCapabilityResolver) Resolve(originalModel string) ([]string, bool) {
    key := normalizeReasoningModelName(originalModel)
    if current := r.overrides.Load(); current != nil {
        if values, ok := (*current)[key]; ok { return slices.Clone(values), true }
    }
    values, ok := builtInModelReasoningEfforts[key]
    return slices.Clone(values), ok
}
```

`parseModelReasoningEffortOverrides` 必须拒绝非对象、超过 500 项、空名称、超过 255 字符的名称、非数组值和未知强度；`ResolveAll` 对全部已知集合求交集，任一未知即返回 `known=false`。

- [ ] **步骤 4：运行解析器测试确认通过**

运行同步骤 2 命令。

预期：相关测试全部 PASS。

- [ ] **步骤 5：提交解析器**

```bash
rtk git add internal/app/model_reasoning_capabilities.go internal/app/model_reasoning_capabilities_test.go
rtk git commit -m "feat(models): add reasoning capability resolver"
```

### 任务 2：持久化覆盖配置并支持热更新

**文件：**
- 修改：`internal/storage/migrate.go`
- 修改：`internal/storage/migrate_sqlite_test.go`
- 修改：`internal/storage/migrate_postgres_test.go`
- 修改：`internal/app/server.go`
- 修改：`internal/app/admin_settings.go`
- 修改：`internal/app/admin_settings_handler_test.go`

- [ ] **步骤 1：编写失败的迁移和设置验证测试**

在 SQLite/PostgreSQL 迁移测试中断言：

```go
var value, valueType, defaultValue string
err := db.QueryRowContext(ctx, `
    SELECT value, value_type, default_value
    FROM system_settings
    WHERE key = 'model_reasoning_effort_overrides'
`).Scan(&value, &valueType, &defaultValue)
if err != nil { t.Fatal(err) }
if value != "{}" || valueType != "json" || defaultValue != "{}" {
    t.Fatalf("reasoning overrides=%q/%q/%q", value, valueType, defaultValue)
}
```

在 `admin_settings_handler_test.go` 增加表驱动测试：合法对象返回 200；未知强度、数组顶层、空模型名和 501 项对象返回 400；只更新该设置不会调用 `triggerRestart`，并且服务器解析器立即返回新值。

- [ ] **步骤 2：运行定向测试确认失败**

```bash
rtk go test -tags sonic ./internal/storage ./internal/app -run 'ReasoningEffort|ReasoningCapability' -count=1
```

预期：迁移设置缺失，JSON 设置被报告为 unknown，Server 尚无解析器字段。

- [ ] **步骤 3：增加默认系统设置和 Server 初始化**

在 `migrate.go` 默认设置切片加入：

```go
{modelReasoningEffortOverridesSetting, "{}", "json", "按原模型名称覆盖可用推理强度", "{}"},
```

由于 storage 包不能依赖 app 包，迁移文件使用同值字符串常量。在 Server 中新增：

```go
modelReasoningCapabilities *modelReasoningCapabilityResolver
```

构造 Server 前解析 `configService.GetString(modelReasoningEffortOverridesSetting, "{}")`；历史脏值记录警告并以 `{}` 初始化，不能阻止启动。

- [ ] **步骤 4：实现设置校验和条件重启**

在 `validateSettingValue` 的 `json` 分支加入：

```go
case modelReasoningEffortOverridesSetting:
    _, err := parseModelReasoningEffortOverrides(value)
    return err
```

新增：

```go
func (s *Server) applyLiveSetting(key, value string) error {
    if key != modelReasoningEffortOverridesSetting { return nil }
    return s.modelReasoningCapabilities.SetOverrides(value)
}

func settingsRequireRestart(updates map[string]string) bool {
    for key := range updates {
        if key != modelReasoningEffortOverridesSetting { return true }
    }
    return false
}
```

单项更新、重置和批量更新在数据库写成功后调用 `applyLiveSetting`。仅包含推理覆盖设置时返回“已保存并立即生效”且不启动重启 goroutine；与其他设置混合保存时立即更新解析器并保持现有两秒重启。

- [ ] **步骤 5：运行迁移和设置测试确认通过**

运行步骤 2 命令，预期全部 PASS。

- [ ] **步骤 6：提交设置能力**

```bash
rtk git add internal/storage/migrate.go internal/storage/migrate_sqlite_test.go internal/storage/migrate_postgres_test.go internal/app/server.go internal/app/admin_settings.go internal/app/admin_settings_handler_test.go
rtk git commit -m "feat(settings): add live reasoning effort overrides"
```

### 任务 3：扩展 Token 可见的 `/v1/models` 响应

**文件：**
- 修改：`internal/app/proxy_gemini.go`
- 修改：`internal/app/proxy_gemini_test.go`

- [ ] **步骤 1：编写失败的 Handler 测试**

创建渠道：

```go
ModelEntries: []model.ModelEntry{
    {Model: "sciland-3.0", RedirectModel: "gpt-5.6-sol"},
}
```

分别以普通 OpenAI、`User-Agent: codex-cli/1.0` 和 `anthropic-version` 请求 `/v1/models`，断言 `supported_reasoning_efforts` 为 `low/medium/high/xhigh`。再增加：

- 同一公开模型映射两个原模型时返回交集。
- 候选原模型存在未知能力时省略字段。
- 人工空数组保留 `[]`，不能被 `omitempty` 丢失。
- Token 拒绝某个渠道时，该渠道的原模型不能影响能力交集。
- `/v1beta/models` 响应不出现新字段。

- [ ] **步骤 2：运行 Handler 测试确认失败**

```bash
rtk go test -tags sonic ./internal/app -run 'TestProxyGemini_ListModelsHandlers' -count=1
```

预期：模型对象中没有 `supported_reasoning_efforts`。

- [ ] **步骤 3：实现可见渠道能力计算**

将模型列表加载改为一次获取渠道：

```go
channels, err := s.GetEnabledChannelsByModel(ctx, "*")
models := modelNamesFromChannels(channels)
models = s.filterVisibleModelsForRequest(c, clientProtocol, models)
capabilities := s.reasoningCapabilitiesForVisibleModels(c, channels, models)
```

`reasoningCapabilitiesForVisibleModels` 必须先应用当前 Token 的渠道限制，再遍历未禁用的 `ModelEntries`，用 `RedirectModel` 或 `Model` 构建公开模型到原模型集合，最后调用解析器 `ResolveAll`。

为区分“未知”和“明确空集合”，响应模型使用指针：

```go
type ModelInfo struct {
    ID                        string    `json:"id"`
    Object                    string    `json:"object"`
    Created                   int64     `json:"created"`
    OwnedBy                   string    `json:"owned_by"`
    SupportedReasoningEfforts *[]string `json:"supported_reasoning_efforts,omitempty"`
}
```

Anthropic 模型对象加入同一指针字段；Codex 继续使用 OpenAI 形态。

- [ ] **步骤 4：运行模型列表测试确认通过**

运行步骤 2 命令，预期全部 PASS。

- [ ] **步骤 5：提交 API 扩展**

```bash
rtk git add internal/app/proxy_gemini.go internal/app/proxy_gemini_test.go
rtk git commit -m "feat(models): expose supported reasoning efforts"
```

### 任务 4：增加管理后台全局映射编辑器

**文件：**
- 创建：`web/assets/js/model-reasoning-efforts.js`
- 创建：`web/assets/js/model-reasoning-efforts.test.js`
- 修改：`web/assets/js/settings.js`
- 修改：`web/assets/js/settings.test.js`
- 修改：`web/settings.html`
- 修改：`web/assets/css/styles.css`
- 修改：`web/assets/locales/zh-CN.js`
- 修改：`web/assets/locales/en.js`

- [ ] **步骤 1：为纯 JavaScript 模块编写失败测试**

```js
test('normalizes model reasoning effort overrides', () => {
  assert.deepEqual(normalizeOverrides({
    ' GPT-5.6-SOL ': ['HIGH', 'low', 'high']
  }), {
    'gpt-5.6-sol': ['low', 'high']
  });
});

test('preserves explicit empty effort arrays', () => {
  assert.deepEqual(normalizeOverrides({ 'no-reasoning': [] }), {
    'no-reasoning': []
  });
});
```

测试还要覆盖未知强度、空名称、超长名称、添加、更新、删除和按模型名称排序。

- [ ] **步骤 2：运行前端测试确认失败**

```bash
rtk node --test web/assets/js/model-reasoning-efforts.test.js web/assets/js/settings.test.js
```

预期：模块不存在或导出函数未定义。

- [ ] **步骤 3：实现纯映射模块**

模块导出并挂载浏览器全局：

```js
const REASONING_EFFORT_ORDER = ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'];
const REASONING_EFFORT_SET = new Set(REASONING_EFFORT_ORDER);

function normalizeOverrides(value) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new TypeError('overrides must be an object');
  }
  const normalized = {};
  for (const [rawModel, rawEfforts] of Object.entries(value)) {
    const model = String(rawModel).trim().toLowerCase();
    if (!model || model.length > 255) throw new TypeError('invalid model name');
    if (!Array.isArray(rawEfforts)) throw new TypeError('efforts must be an array');
    const selected = new Set(rawEfforts.map((effort) => String(effort).trim().toLowerCase()));
    for (const effort of selected) {
      if (!REASONING_EFFORT_SET.has(effort)) throw new TypeError(`unknown effort: ${effort}`);
    }
    normalized[model] = REASONING_EFFORT_ORDER.filter((effort) => selected.has(effort));
  }
  return Object.fromEntries(Object.entries(normalized).sort(([a], [b]) => a.localeCompare(b)));
}

function upsertOverride(value, model, efforts) {
  return normalizeOverrides({ ...normalizeOverrides(value), [model]: efforts });
}

function deleteOverride(value, model) {
  const normalized = normalizeOverrides(value);
  delete normalized[String(model).trim().toLowerCase()];
  return normalized;
}

const api = {
  REASONING_EFFORT_ORDER,
  normalizeOverrides,
  upsertOverride,
  deleteOverride
};

if (typeof module !== 'undefined') {
  module.exports = api;
}
if (typeof window !== 'undefined') window.ModelReasoningEfforts = api;
```

- [ ] **步骤 4：实现设置页弹窗**

在 `settings.html` 引入新脚本并新增具备焦点恢复、关闭按钮、模型输入、七个 checkbox、覆盖列表、删除按钮和“应用到设置”按钮的 modal。`settings.js` 对 `model_reasoning_effort_overrides` 渲染只读摘要与“编辑映射”按钮；应用时将规范化对象序列化到隐藏 input，并调用现有 `markChanged`。

设置页保存仍走 `/admin/settings/batch`。仅该设置变化时后端响应立即生效，不展示重启倒计时；混合保存沿用现有消息。

- [ ] **步骤 5：补齐中英文文案和样式**

中文至少包含“原模型推理强度覆盖”“原模型名称”“可用推理强度”“恢复自动判断”“没有人工覆盖”；英文提供对应文本。样式复用现有 modal、form-input 和 btn，仅新增响应式覆盖行与强度 checkbox 布局。

- [ ] **步骤 6：运行前端测试确认通过**

```bash
rtk node --test web/assets/js/model-reasoning-efforts.test.js web/assets/js/settings.test.js
rtk make verify-web
```

预期：Node 定向测试及全部 Web 校验 PASS。

- [ ] **步骤 7：提交管理后台**

```bash
rtk git add -f web/assets/js/model-reasoning-efforts.js web/assets/js/model-reasoning-efforts.test.js web/assets/js/settings.js web/assets/js/settings.test.js web/settings.html web/assets/css/styles.css web/assets/locales/zh-CN.js web/assets/locales/en.js
rtk git commit -m "feat(web): manage reasoning effort overrides"
```

### 任务 5：更新接口说明并完成全量验证

**文件：**
- 修改：`web/openapi.yaml`
- 修改：`docs/superpowers/verification/2026-08-09-v4.6.10-beta.2-custom.md`

- [ ] **步骤 1：更新接口说明**

在 OpenAPI 说明中记录系统设置 `model_reasoning_effort_overrides` 的 JSON 对象结构和允许强度，并加入 `/v1/models` 响应扩展示例：

```yaml
supported_reasoning_efforts:
  type: array
  items:
    type: string
    enum: [none, minimal, low, medium, high, xhigh, max]
```

明确能力未知时字段省略，明确空集合时返回 `[]`，且 `redirect_model` 不对外暴露。

- [ ] **步骤 2：运行后端全量验证**

```bash
rtk go test -tags sonic ./internal/...
rtk make race-fast
rtk golangci-lint run ./...
```

预期：3,861 项以上测试全部通过，race 无数据竞争，lint 无问题。

- [ ] **步骤 3：运行 Web 和仓库验证**

```bash
rtk make verify-web
rtk git diff --check
rtk rg -n 'authz|X-Sedna-Env' web/openapi.yaml internal/app/model_reasoning_capabilities.go
```

预期：Web 校验和 diff 检查通过；最后一个扫描命令无输出，确认本功能没有重新引入 authz。

- [ ] **步骤 4：构建并在本地副本冒烟**

```bash
rtk make build VERSION=v4.6.10-beta.2-custom
```

用测试数据库创建 `sciland-3.0 → gpt-5.6-sol` 渠道，调用 `/v1/models` 并断言：

```json
"supported_reasoning_efforts": ["low", "medium", "high", "xhigh"]
```

保存人工覆盖 `{"gpt-5.6-sol":["low","high"]}` 后再次调用，必须立即返回 `low/high` 且进程 PID 不变。

- [ ] **步骤 5：记录验证结果并提交**

将测试数量、race、lint、Web 校验、构建 SHA-256 和两次冒烟结果追加到验收记录，不写入管理员密码、API Token 或数据库 DSN。

```bash
rtk git add -f web/openapi.yaml docs/superpowers/verification/2026-08-09-v4.6.10-beta.2-custom.md
rtk git commit -m "docs: document model reasoning capabilities"
```

## 完成标准

- `sciland-3.0` 自动继承 `gpt-5.6-sol` 的 `low/medium/high/xhigh`。
- 管理员可以按原模型名称全局覆盖并立即生效。
- OpenAI、Codex 和 Anthropic `/v1/models` 返回增量能力字段。
- 未知、明确空集合、多渠道交集和 Token 渠道隔离语义均有测试。
- `/v1beta/models`、请求推理参数转换、计费和 authz 均未改变。
- 后端、race、lint、Web、构建和副本冒烟全部通过。
