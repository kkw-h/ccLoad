# 模型列表元数据实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 在 OpenAI/Codex 与 Anthropic 两种 `/v1/models` 响应中，以公开模型映射后的原模型为能力来源，安全返回展示名、供应商、推理强度、上下文、最大输出和输入类型。

**架构：** 扩展内嵌 CLIProxy 目录的只读模型结构，并在 app 层新增支持原子热更新的元数据解析器。模型列表 Handler 对当前 Token 可见渠道进行逐字段保守聚合，再使用共享 DTO 生成两种兼容响应；人工覆盖使用独立 JSON 系统设置。

**技术栈：** Go、Gin、SQLite/PostgreSQL、内嵌 CLIProxy JSON、原子快照、OpenAPI 3.1、原生 JavaScript。

---

## 文件结构

- 修改 `internal/protocol/cliproxy/registry/models.go`：保留内嵌目录已有的供应商、展示名、上下文和最大输出字段，并在克隆时复制切片。
- 创建 `internal/protocol/cliproxy/registry/models_test.go`：验证内嵌字段解析与返回副本隔离。
- 创建 `internal/app/model_metadata_capabilities.go`：严格解析人工覆盖、合并内建目录、按多个原模型逐字段聚合。
- 创建 `internal/app/model_metadata_capabilities_test.go`：覆盖解析、继承、未知值和聚合边界。
- 修改 `internal/app/server.go`：初始化并持有元数据解析器。
- 修改 `internal/app/admin_settings.go`：校验、热应用并判定元数据设置无需重启。
- 修改 `internal/app/admin_settings_handler_test.go`：覆盖管理设置所有写路径和运行时快照。
- 修改 `internal/storage/migrate.go`、`internal/storage/migrate_sqlite_test.go`、`internal/storage/migrate_postgres_test.go`：持久化默认设置并验证两种数据库。
- 修改 `internal/app/proxy_gemini.go`：聚合能力并扩展 OpenAI/Codex 与 Anthropic 模型 DTO。
- 修改 `internal/app/proxy_gemini_test.go`：验证两种响应、映射、权限和聚合规则。
- 修改 `web/assets/js/settings.js`、`web/assets/js/settings.test.js`：将元数据设置归入高级区且视为即时生效设置。
- 修改 `web/assets/locales/zh-CN.js`、`web/assets/locales/en.js`：补充设置说明。
- 修改 `web/openapi.yaml`、`internal/app/openapi_test.go`：记录新设置和模型响应契约。

### 任务 1：暴露内嵌 CLIProxy 模型元数据

**文件：**
- 修改：`internal/protocol/cliproxy/registry/models.go`
- 创建：`internal/protocol/cliproxy/registry/models_test.go`

- [ ] **步骤 1：编写失败的目录字段测试**

创建测试，断言 `LookupModelInfo("gpt-5.6-sol", "openai")` 返回内嵌 JSON 中的精确值：

```go
func TestLookupModelInfoIncludesPublicMetadata(t *testing.T) {
    info := LookupModelInfo("gpt-5.6-sol", "openai")
    if info == nil { t.Fatal("model not found") }
    if info.OwnedBy != "openai" || info.DisplayName != "GPT 5.6 Sol" {
        t.Fatalf("identity metadata=%+v", info)
    }
    if info.ContextLength != 372000 || info.MaxCompletionTokens != 128000 {
        t.Fatalf("token metadata=%+v", info)
    }
    info.SupportedParameters[0] = "mutated"
    fresh := LookupModelInfo("gpt-5.6-sol", "openai")
    if fresh.SupportedParameters[0] != "tools" { t.Fatal("lookup leaked mutable slice") }
}
```

- [ ] **步骤 2：运行测试确认失败**

```bash
rtk go test -tags sonic ./internal/protocol/cliproxy/registry -run TestLookupModelInfoIncludesPublicMetadata -count=1
```

预期：编译失败，提示 `ModelInfo` 没有新字段。

- [ ] **步骤 3：扩展只读目录结构**

在 `ModelInfo` 中加入：

```go
OwnedBy             string   `json:"owned_by,omitempty"`
DisplayName         string   `json:"display_name,omitempty"`
ContextLength       int64    `json:"context_length,omitempty"`
MaxCompletionTokens int64    `json:"max_completion_tokens,omitempty"`
SupportedParameters []string `json:"supported_parameters,omitempty"`
```

在 `cloneModelInfo` 中使用 `append([]string(nil), model.SupportedParameters...)` 克隆新切片。不得修改 `models.json` 的上游快照内容。

- [ ] **步骤 4：运行测试确认通过并提交**

```bash
rtk go test -tags sonic ./internal/protocol/cliproxy/registry -count=1
rtk git add internal/protocol/cliproxy/registry/models.go internal/protocol/cliproxy/registry/models_test.go
rtk git commit -m "feat(models): expose embedded model metadata"
```

预期：测试 PASS，提交成功。

### 任务 2：实现元数据覆盖和保守聚合解析器

**文件：**
- 创建：`internal/app/model_metadata_capabilities.go`
- 创建：`internal/app/model_metadata_capabilities_test.go`

- [ ] **步骤 1：编写失败的解析与聚合测试**

测试至少覆盖以下精确行为：

```go
resolver, err := newModelMetadataResolver(`{
  "gpt-5.6-sol": {
    "provider": "OpenAI",
    "contextWindow": 300000,
    "maxTokens": 64000,
    "inputTypes": [" IMAGE ", "text", "image"]
  }
}`)
if err != nil { t.Fatal(err) }
got := resolver.Resolve("GPT-5.6-SOL")
// 人工值覆盖目录；inputTypes 规范化为 [image, text]。
```

另写表驱动用例拒绝：顶层数组、501 个模型、空模型名、超过 255 字符模型名、未知字段、空供应商、零或负数数值、非数组输入类型、空输入类型字符串和尾随 JSON。聚合测试断言：相同供应商保留，不同供应商为 `mixed`；数值取最小；输入类型取交集；任一字段未知时只省略该字段；明确空输入类型保留空数组。

- [ ] **步骤 2：运行测试确认失败**

```bash
rtk go test -tags sonic ./internal/app -run 'TestModelMetadata' -count=1
```

预期：编译失败，提示解析器未定义。

- [ ] **步骤 3：实现最小解析器**

定义：

```go
const modelMetadataOverridesSetting = "model_metadata_overrides"

type modelMetadata struct {
    Provider      *string
    ContextWindow *int64
    MaxTokens     *int64
    InputTypes    *[]string
}

type modelMetadataResolver struct {
    overrides atomic.Pointer[map[string]modelMetadata]
}
```

`Resolve` 先从 `registry.LookupModelInfo` 建立内建值，再逐字段覆盖人工配置。供应商显示名使用固定映射：`openai -> OpenAI`、`anthropic -> Anthropic`、`google -> Google`、`xai -> xAI`，其他非空值保留目录原值。为 `gpt-5.6-sol` 增加明确的内建输入类型 `[]string{"text"}`；不得从模型名称猜测图片能力。

JSON 解码使用 `json.Decoder` 和 `DisallowUnknownFields`，保留指针以区分“未覆盖”与“明确空数组”。规范化输入类型后按字典序输出。`ResolveAll` 对每个字段独立聚合，不因一个字段未知而丢弃其他字段。

- [ ] **步骤 4：运行测试确认通过并提交**

```bash
rtk go test -tags sonic ./internal/app -run 'TestModelMetadata' -count=1
rtk git add internal/app/model_metadata_capabilities.go internal/app/model_metadata_capabilities_test.go
rtk git commit -m "feat(models): add metadata capability resolver"
```

预期：相关测试全部 PASS。

### 任务 3：持久化覆盖并支持无重启热更新

**文件：**
- 修改：`internal/storage/migrate.go`
- 修改：`internal/storage/migrate_sqlite_test.go`
- 修改：`internal/storage/migrate_postgres_test.go`
- 修改：`internal/app/server.go`
- 修改：`internal/app/admin_settings.go`
- 修改：`internal/app/admin_settings_handler_test.go`

- [ ] **步骤 1：编写失败的迁移和设置测试**

SQLite 与 PostgreSQL 测试均断言 `model_metadata_overrides` 的 `value`、`value_type`、`default_value` 分别是 `{}`、`json`、`{}`。管理 Handler 测试断言合法更新和重置立即改变 `Server.modelMetadataCapabilities`，不调用 `RestartFunc`；非法 JSON 返回 400；与 `log_retention_days` 混合批量保存时两个解析器均热更新且仍触发一次重启。

- [ ] **步骤 2：运行定向测试确认失败**

```bash
rtk go test -tags sonic ./internal/storage ./internal/app -run 'ModelMetadata|MetadataOverrides' -count=1
```

预期：设置缺失，Server 没有元数据解析器字段。

- [ ] **步骤 3：增加设置与 Server 初始化**

迁移默认设置加入：

```go
{"model_metadata_overrides", "{}", "json", "按原模型名称覆盖模型列表元数据", "{}"},
```

`Server` 加入 `modelMetadataCapabilities *modelMetadataResolver`。`NewServer` 使用配置值初始化；历史脏值记录警告并回退 `{}`，不能阻止启动。

- [ ] **步骤 4：扩展校验、热应用和重启判定**

`validateSettingValue` 的 JSON 分支调用 `parseModelMetadataOverrides`。`applyLiveSettings` 分别应用请求中存在的推理覆盖与元数据覆盖。`settingsRequireRestart` 将两个键都视为即时生效：

```go
func isLiveModelCapabilitySetting(key string) bool {
    return key == modelReasoningEffortOverridesSetting || key == modelMetadataOverridesSetting
}
```

所有解析已在数据库写入前验证；热应用仍返回错误并记录日志，避免静默运行时不一致。

- [ ] **步骤 5：运行定向测试确认通过并提交**

```bash
rtk go test -tags sonic ./internal/storage ./internal/app -run 'ModelMetadata|MetadataOverrides|ReasoningEffortOverridesMixedBatch' -count=1
rtk git add internal/storage/migrate.go internal/storage/migrate_sqlite_test.go internal/storage/migrate_postgres_test.go internal/app/server.go internal/app/admin_settings.go internal/app/admin_settings_handler_test.go
rtk git commit -m "feat(settings): add live model metadata overrides"
```

预期：相关测试全部 PASS。

### 任务 4：扩展两种 `/v1/models` 响应

**文件：**
- 修改：`internal/app/proxy_gemini.go`
- 修改：`internal/app/proxy_gemini_test.go`

- [ ] **步骤 1：编写失败的模型列表测试**

建立 `sciland-3.0 -> gpt-5.6-sol` 渠道，分别发送普通、`User-Agent: codex-cli/1.0` 和带 `anthropic-version` 的请求，断言三种客户端均返回：

```json
{
  "displayName": "Sciland 3.0",
  "provider": "OpenAI",
  "thinkingLevels": ["low", "medium", "high", "xhigh"],
  "contextWindow": 372000,
  "maxTokens": 128000,
  "inputTypes": ["text"]
}
```

同时断言原 `supported_reasoning_efforts` 未改变，Anthropic 的 `display_name` 等于 `displayName`。补充多渠道字段级聚合、未知字段省略、显式空数组、Token 渠道限制、`none -> off` 仅影响 `thinkingLevels`，以及 `/v1beta/models` 没有新增元数据字段的用例。

- [ ] **步骤 2：运行 Handler 测试确认失败**

```bash
rtk go test -tags sonic ./internal/app -run 'TestProxyGemini_ListModelsHandlers|TestModelListMetadata' -count=1
```

预期：响应缺少 camelCase 元数据。

- [ ] **步骤 3：实现共享聚合结果和响应字段**

新增响应专用结构：

```go
type visibleModelMetadata struct {
    DisplayName   string
    Provider      *string
    Thinking      *[]string
    ContextWindow *int64
    MaxTokens     *int64
    InputTypes    *[]string
}
```

重用 `visibleModelsAndChannelsForRequest` 的可见渠道，在一次遍历中构建公开模型到去重原模型集合。推理强度调用现有解析器；元数据调用新解析器。`thinkingLevels` 克隆 `supported_reasoning_efforts`，仅将值 `none` 改为 `off`，不得修改原切片。

OpenAI/Codex 与 Anthropic DTO 均加入带 `omitempty` 的 `provider`、`thinkingLevels`、`contextWindow`、`maxTokens`、`inputTypes`；`displayName` 必须始终返回。Anthropic 继续保留 `display_name`。

- [ ] **步骤 4：运行测试确认通过并提交**

```bash
rtk go test -tags sonic ./internal/app -run 'TestProxyGemini_ListModelsHandlers|TestModelListMetadata' -count=1
rtk git add internal/app/proxy_gemini.go internal/app/proxy_gemini_test.go
rtk git commit -m "feat(models): expose mapped model metadata"
```

预期：两种响应和所有边界用例 PASS。

### 任务 5：更新管理页面与 OpenAPI，并完成验证

**文件：**
- 修改：`web/assets/js/settings.js`
- 修改：`web/assets/js/settings.test.js`
- 修改：`web/assets/locales/zh-CN.js`
- 修改：`web/assets/locales/en.js`
- 修改：`web/openapi.yaml`
- 修改：`internal/app/openapi_test.go`

- [ ] **步骤 1：编写失败的页面和 OpenAPI 测试**

页面测试断言 `model_metadata_overrides` 位于高级分组，单独保存时不弹重启确认，与普通设置混合时仍确认重启。OpenAPI 测试解析 schema 并断言 `OpenAIModel` 与 `AnthropicModel` 都声明六个新字段，且 Anthropic 仍要求 `display_name`。

- [ ] **步骤 2：运行测试确认失败**

```bash
rtk node --test web/assets/js/settings.test.js
rtk go test -tags sonic ./internal/app -run OpenAPI -count=1
```

预期：新设置分组或 schema 断言失败。

- [ ] **步骤 3：更新页面和文档**

在 `settings.js` 增加 `modelMetadataOverridesSettingKey`，加入高级分组，并让前端重启判断与服务端的两个即时设置保持一致。中英文 locale 增加 `settings.desc.model_metadata_overrides`。

OpenAPI 新增 `ModelMetadataOverrides`、`ThinkingLevel` 和共享 `ModelListMetadata` schema；`OpenAIModel`、`AnthropicModel` 通过属性引用记录 camelCase 字段。示例使用 `sciland-3.0`，并说明未知字段省略、多渠道聚合及 `none -> off` 规则。

- [ ] **步骤 4：运行定向测试和格式检查**

```bash
rtk node --test web/assets/js/settings.test.js
rtk go test -tags sonic ./internal/app -run OpenAPI -count=1
rtk git diff --check
```

预期：全部 PASS，`git diff --check` 无输出。

- [ ] **步骤 5：运行完整验证**

```bash
rtk go test -tags sonic ./internal/protocol/cliproxy/registry ./internal/storage ./internal/app -count=1
rtk make verify-web
rtk golangci-lint run ./...
rtk make race-fast
```

预期：Go 测试、Web 验证、静态检查和 race-fast 全部成功。

- [ ] **步骤 6：提交文档与页面**

```bash
rtk git add web/assets/js/settings.js web/assets/js/settings.test.js web/assets/locales/zh-CN.js web/assets/locales/en.js internal/app/openapi_test.go
rtk git add -f web/openapi.yaml
rtk git commit -m "docs(models): document model list metadata"
```

预期：提交成功，工作区只剩既有未跟踪构建制品。
