package app

import (
	"net/http"
	"testing"

	"ccLoad/internal/model"
)

// buildAttemptUsageEvent 应复用 LogEntry 上已归一化的 usage/cost，逐字段一致。
func TestBuildLogEntry_UsageEventReusesNormalizedFields(t *testing.T) {
	t.Parallel()

	entry := buildLogEntry(logEntryParams{
		RequestModel:   "claude-sonnet",
		ActualModel:    "claude-sonnet-real",
		StatusCode:     http.StatusOK,
		ChannelID:      42,
		AuthTokenID:    7,
		ClientIP:       "1.2.3.4",
		CostMultiplier: 2,
		TokenHash:      "hash-abc",
		RequestID:      "req-1",
		AttemptSeq:     3,
		Result: &fwResult{
			InputTokens:          100,
			OutputTokens:         200,
			CacheReadInputTokens: 10,
		},
	})

	ev := entry.UsageEvent
	if ev == nil {
		t.Fatal("UsageEvent 应被构建")
	}
	if ev.Kind != model.UsageEventAttempt {
		t.Fatalf("kind=%q, want attempt", ev.Kind)
	}
	if ev.RequestID != "req-1" || ev.AttemptSeq != 3 || ev.TokenHash != "hash-abc" {
		t.Fatalf("关联字段不符: %+v", ev)
	}
	// usage 逐字段复用 entry。
	if ev.InputTokens != entry.InputTokens || ev.OutputTokens != entry.OutputTokens ||
		ev.CacheReadInputTokens != entry.CacheReadInputTokens {
		t.Fatalf("usage 未复用 LogEntry: ev=%+v entry.in=%d", ev, entry.InputTokens)
	}
	// 成本复用 + effective = cost * multiplier。
	if ev.StandardCostUSD != entry.Cost || ev.CostMultiplier != entry.CostMultiplier {
		t.Fatalf("cost 未复用: ev.std=%v entry.cost=%v", ev.StandardCostUSD, entry.Cost)
	}
	if want := entry.Cost * entry.CostMultiplier; ev.EffectiveCostUSD != want {
		t.Fatalf("effective=%v, want %v", ev.EffectiveCostUSD, want)
	}
}

// 非代理路径（无 request_id）不应产生用量事件。
func TestBuildLogEntry_NoUsageEventWithoutRequestID(t *testing.T) {
	t.Parallel()

	entry := buildLogEntry(logEntryParams{
		RequestModel: "m",
		StatusCode:   http.StatusOK,
		Result:       &fwResult{InputTokens: 1, OutputTokens: 2},
	})
	if entry.UsageEvent != nil {
		t.Fatalf("无 request_id 时不应产生用量事件, got %+v", entry.UsageEvent)
	}
}

// 499 客户端取消但上游已产 usage：日志行与事件应携带真实 usage/cost（决策一）。
func TestBuildLogEntry_ClientCancel499CarriesUsage(t *testing.T) {
	t.Parallel()

	entry := buildLogEntry(logEntryParams{
		RequestModel: "claude-sonnet",
		StatusCode:   499,
		ChannelID:    1,
		RequestID:    "req-499",
		AttemptSeq:   1,
		ErrMsg:       "context canceled",
		IsStreaming:  true,
		Result: &fwResult{
			InputTokens:   50,
			OutputTokens:  80,
			FirstByteTime: 0.3,
		},
	})

	if entry.InputTokens != 50 || entry.OutputTokens != 80 {
		t.Fatalf("499 日志行应带 usage: in=%d out=%d", entry.InputTokens, entry.OutputTokens)
	}
	if entry.UsageEvent == nil || entry.UsageEvent.OutputTokens != 80 {
		t.Fatalf("499 事件应携带真实 usage: %+v", entry.UsageEvent)
	}
	if entry.UsageEvent.StatusCode != 499 {
		t.Fatalf("事件 status=%d, want 499", entry.UsageEvent.StatusCode)
	}
}

// 非 499 错误（有 ErrMsg）不计费：不应写入 usage/cost（保持"失败只计次"语义）。
func TestBuildLogEntry_Non499ErrorDoesNotBillUsage(t *testing.T) {
	t.Parallel()

	entry := buildLogEntry(logEntryParams{
		RequestModel: "claude-sonnet",
		StatusCode:   599,
		RequestID:    "req-599",
		AttemptSeq:   1,
		ErrMsg:       "stream interrupted",
		IsStreaming:  true,
		Result: &fwResult{
			InputTokens:  50,
			OutputTokens: 80,
		},
	})

	if entry.InputTokens != 0 || entry.OutputTokens != 0 || entry.Cost != 0 {
		t.Fatalf("非 499 错误不应计费: in=%d out=%d cost=%v", entry.InputTokens, entry.OutputTokens, entry.Cost)
	}
	if ev := entry.UsageEvent; ev == nil || ev.OutputTokens != 0 || ev.StandardCostUSD != 0 {
		t.Fatalf("非 499 错误事件应为零 usage/cost: %+v", ev)
	}
}
