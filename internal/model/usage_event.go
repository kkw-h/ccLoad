package model

// UsageEventKind 区分用量事件的语义层级。
type UsageEventKind string

const (
	// UsageEventAttempt 单次真实上游调用：计费权威，落库成功后发布。
	UsageEventAttempt UsageEventKind = "attempt"
	// UsageEventRequest 用户请求终态汇总：瘦视图，请求收尾即发，不带 usage/cost。
	UsageEventRequest UsageEventKind = "request"
)

// UsageEvent 是发布到 Redis 的用量事件。
//
// 字段全部复用请求结束时已归一化的结果（LogEntry + 少量上下文），
// 不再解析 usage 或重算成本。usage/cost 仅 attempt 事件填充；
// request 事件是导航视图，相关字段留零。
//
// 消费端契约：
//   - attempt = 账本（唯一计费权威）；request = 导航视图。
//   - 二者按 RequestID 关联，禁止对两者同时求和（避免重复计数）。
//   - 允许 request 先于其 attempt 到达（attempt 有批量落库延迟）。
type UsageEvent struct {
	RequestID   string         `json:"request_id"`
	AttemptSeq  int            `json:"attempt_seq"` // request 事件为 0
	Kind        UsageEventKind `json:"kind"`
	Time        JSONTime       `json:"time"`
	Environment string         `json:"environment,omitempty"`
	TokenHash   string         `json:"token_hash,omitempty"`
	AuthTokenID int64          `json:"auth_token_id"`
	ChannelID   int64          `json:"channel_id"`
	Model       string         `json:"model"`
	ActualModel string         `json:"actual_model,omitempty"`
	StatusCode  int            `json:"status_code"`
	IsStreaming bool           `json:"is_streaming"`
	Duration    float64        `json:"duration"`
	FirstByte   float64        `json:"first_byte_time,omitempty"`
	ClientIP    string         `json:"client_ip,omitempty"`

	// 以下仅 attempt 事件填充（request 事件留零）。
	InputTokens              int     `json:"input_tokens"`
	OutputTokens             int     `json:"output_tokens"`
	ReasoningTokens          int     `json:"reasoning_tokens,omitempty"`
	CacheReadInputTokens     int     `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int     `json:"cache_creation_input_tokens"`
	Cache5mInputTokens       int     `json:"cache_5m_input_tokens,omitempty"`
	Cache1hInputTokens       int     `json:"cache_1h_input_tokens,omitempty"`
	StandardCostUSD          float64 `json:"standard_cost_usd"`  // = LogEntry.Cost
	CostMultiplier           float64 `json:"cost_multiplier"`    // = LogEntry.CostMultiplier
	EffectiveCostUSD         float64 `json:"effective_cost_usd"` // = Cost * CostMultiplier
	ServiceTier              string  `json:"service_tier,omitempty"`
}
