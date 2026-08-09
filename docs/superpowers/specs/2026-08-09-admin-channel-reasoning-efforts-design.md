# 管理渠道推理强度响应设计

## 目标

让以下管理接口在每个 `models` 条目中返回 `supported_reasoning_efforts`：

- `GET /admin/channels`
- `GET /admin/channels/:id`
- `GET /admin/channels/:id/editor`

返回能力必须与 `/v1/models` 使用同一个运行时能力解析器，并依据渠道条目的原模型名称判断。

## 响应契约

已知能力的模型条目示例：

```json
{
  "model": "sciland-3.0",
  "redirect_model": "gpt-5.6-sol",
  "supported_reasoning_efforts": ["low", "medium", "high", "xhigh"]
}
```

解析顺序：

1. `redirect_model` 非空时以它作为原模型名称。
2. `redirect_model` 为空时以 `model` 作为原模型名称。
3. 管理后台的 `model_reasoning_effort_overrides` 优先于内建能力表。
4. 能力未知时省略 `supported_reasoning_efforts`。
5. 明确配置为空数组时返回 `supported_reasoning_efforts: []`。

管理接口按单个渠道模型条目解析，不执行 `/v1/models` 面向多渠道别名的能力交集计算。

## 实现边界

不修改持久化结构 `model.ModelEntry`，也不改变创建、更新、导入接口的请求格式。新增管理响应专用模型条目 DTO，并由一个共享转换函数生成带能力信息的模型列表。

`ChannelWithCooldown` 继续承载列表、详情和编辑器响应，但在序列化时用响应专用的 `models` 字段覆盖嵌入 `model.Config` 的持久化模型列表。列表和详情均调用同一个转换方法；编辑器复用详情结果，因此三者保持一致。

## 测试

测试覆盖：

- 列表接口根据 `redirect_model` 返回能力。
- 详情接口在无重定向时根据 `model` 返回能力。
- 编辑器接口复用同一响应结构。
- 未知模型省略字段。
- 显式空数组覆盖返回 `[]`。
- 请求及存储使用的 `model.ModelEntry` 不新增响应字段。
