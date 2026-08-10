# 模型列表元数据响应设计

## 目标

扩展 `GET /v1/models` 的 OpenAI/Codex 和 Anthropic 两种响应，为每个客户端可见模型附加以下 camelCase 元数据：

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

现有标准字段和 `supported_reasoning_efforts` 保持不变。Anthropic 响应继续保留 `display_name`，并额外返回同值的 `displayName`，避免破坏现有客户端。

## 数据来源与优先级

公开模型名称与渠道中的原模型名称分开处理：

1. 响应 `id` 使用公开模型名称，例如 `sciland-3.0`。
2. `displayName` 默认由公开模型名称格式化，例如 `Sciland 3.0`。
3. 渠道条目的 `redirect_model` 非空时，它是能力查询使用的原模型；否则使用 `model`。
4. `thinkingLevels` 复用现有 `modelReasoningCapabilityResolver`，因此继续遵守 `model_reasoning_effort_overrides` 和内建推理强度表。
5. `provider`、`contextWindow`、`maxTokens` 和 `inputTypes` 由新的模型元数据解析器按原模型名称查询。人工配置优先，内建 CLIProxy 模型目录作为回退。
6. 内建目录没有可靠输入类型时不推测 `inputTypes`；管理员可通过人工覆盖补充。

新增系统设置 `model_metadata_overrides`，值为以原模型名称为键的 JSON 对象：

```json
{
  "gpt-5.6-sol": {
    "provider": "OpenAI",
    "contextWindow": 372000,
    "maxTokens": 128000,
    "inputTypes": ["text"]
  }
}
```

允许的字段只有 `provider`、`contextWindow`、`maxTokens` 和 `inputTypes`。模型名称和输入类型会去除首尾空白并转为小写；输入类型去重并稳定排序。数值必须为正整数，供应商名称不能为空。对象最多包含 500 个模型，每个模型名称最长 255 个字符。未知字段和结构错误返回 HTTP 400。空对象恢复内建行为。

`displayName` 不从原模型覆盖，始终描述公开模型，避免 `sciland-3.0` 因映射到 `gpt-5.6-sol` 而显示成 `GPT 5.6 Sol`。

## 能力聚合

同一个公开模型可能经多个可见渠道映射到不同原模型。聚合只考虑当前 Token 有权访问的渠道及未停用模型条目：

- `thinkingLevels`：对全部原模型的已知推理强度取交集；输出时仅把 `none` 翻译为 `off`，其他值保持不变。
- `provider`：全部已知且相同时返回该供应商；已知值不一致时返回 `mixed`。
- `contextWindow`：全部原模型均已知时取最小值。
- `maxTokens`：全部原模型均已知时取最小值。
- `inputTypes`：全部原模型均已知时取交集。
- 任一原模型缺少某一字段时，仅省略该字段，不影响其他已知字段。

明确配置的空 `inputTypes` 返回 `[]`；未知输入类型则省略字段。`thinkingLevels` 同样保留现有“明确空数组”和“未知省略”的区别。

## 响应契约

OpenAI/Codex 响应继续返回：

```json
{
  "id": "sciland-3.0",
  "object": "model",
  "created": 0,
  "owned_by": "system",
  "supported_reasoning_efforts": ["low", "medium", "high", "xhigh"],
  "displayName": "Sciland 3.0",
  "provider": "OpenAI",
  "thinkingLevels": ["low", "medium", "high", "xhigh"],
  "contextWindow": 372000,
  "maxTokens": 128000,
  "inputTypes": ["text"]
}
```

Anthropic 响应继续返回 `id`、`display_name`、`type`、`created_at` 和 `supported_reasoning_efforts`，并添加同一组 camelCase 字段。`display_name` 与 `displayName` 始终相同。

所有新增字段均为附加字段；未知能力使用 `omitempty` 省略。`/v1beta/models` 不在本次范围内。

## 运行时更新与管理

`model_metadata_overrides` 作为 JSON 系统设置持久化，可通过现有管理设置接口和设置页面编辑。保存、重置或批量更新后立即替换原子快照，不要求重启 ccLoad；与其他需重启设置一同保存时仍保留原有重启行为。

首版使用设置页面现有 JSON 编辑能力，不新增大型专用表单。服务端校验是最终约束，API 与页面提交得到一致结果。

## 测试

测试覆盖：

- 元数据覆盖 JSON 的归一化、限制、非法结构和并发热更新。
- CLIProxy 内建目录字段可以被元数据解析器读取且返回副本。
- `sciland-3.0 -> gpt-5.6-sol` 继承原模型能力，但显示名称仍为 `Sciland 3.0`。
- OpenAI、Codex 和 Anthropic 三种 `/v1/models` 客户端形态返回一致的 camelCase 元数据。
- Anthropic 同时保留 `display_name`。
- 多渠道聚合遵守交集、最小值、`mixed` 和未知字段省略规则。
- Token 渠道限制不会泄露不可见渠道的能力。
- `none` 只在 `thinkingLevels` 中变成 `off`，`supported_reasoning_efforts` 保持原值。
- `/v1beta/models` 不新增这些字段。
