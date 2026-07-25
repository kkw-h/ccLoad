package eventbus

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/bytedance/sonic"

	"ccLoad/internal/model"
)

func sampleEvent() *model.UsageEvent {
	return &model.UsageEvent{
		RequestID:        "req-1",
		AttemptSeq:       1,
		Kind:             model.UsageEventAttempt,
		Time:             model.JSONTime{Time: time.Unix(1_700_000_000, 0)},
		ChannelID:        7,
		Model:            "claude-x",
		StatusCode:       200,
		InputTokens:      10,
		OutputTokens:     20,
		StandardCostUSD:  0.5,
		CostMultiplier:   2,
		EffectiveCostUSD: 1.0,
	}
}

func TestNew_EmptyDSNReturnsNoop(t *testing.T) {
	p, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := p.(NoopPublisher); !ok {
		t.Fatalf("空 DSN 应返回 NoopPublisher, 实际 %T", p)
	}
}

func TestNew_InvalidDSNErrors(t *testing.T) {
	if _, err := New(Config{DSN: "://not-a-url"}); err == nil {
		t.Fatal("非法 DSN 应返回错误")
	}
}

func TestRedisSink_Stream(t *testing.T) {
	mr := miniredis.RunT(t)
	sink, err := newRedisSink(Config{DSN: "redis://" + mr.Addr(), Stream: "ev", Mode: ModeStream})
	if err != nil {
		t.Fatalf("newRedisSink: %v", err)
	}
	defer func() { _ = sink.Close() }()

	if err := sink.send(context.Background(), sampleEvent()); err != nil {
		t.Fatalf("send: %v", err)
	}

	stream, err := mr.Stream("ev")
	if err != nil {
		t.Fatalf("读取 stream: %v", err)
	}
	if len(stream) != 1 {
		t.Fatalf("stream 条目数=%d, 期望 1", len(stream))
	}
	assertPayload(t, streamFieldValue(t, stream[0].Values, "data"))
}

// streamFieldValue 从 miniredis StreamEntry 的扁平 field/value 切片中取指定字段。
func streamFieldValue(t *testing.T, values []string, field string) string {
	t.Helper()
	for i := 0; i+1 < len(values); i += 2 {
		if values[i] == field {
			return values[i+1]
		}
	}
	t.Fatalf("stream 条目缺少字段 %q: %v", field, values)
	return ""
}

func TestRedisSink_PubSub(t *testing.T) {
	// 仅验证 pubsub 分支被选中且 PUBLISH 被接受；跨连接投递语义属于 redis/go-redis 职责。
	mr := miniredis.RunT(t)
	sink, err := newRedisSink(Config{DSN: "redis://" + mr.Addr(), Stream: "chan", Mode: ModePubSub})
	if err != nil {
		t.Fatalf("newRedisSink: %v", err)
	}
	defer func() { _ = sink.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := sink.send(ctx, sampleEvent()); err != nil {
		t.Fatalf("pubsub send: %v", err)
	}
}

func assertPayload(t *testing.T, payload string) {
	t.Helper()
	var ev model.UsageEvent
	if err := sonic.Unmarshal([]byte(payload), &ev); err != nil {
		t.Fatalf("反序列化事件失败: %v", err)
	}
	if ev.RequestID != "req-1" || ev.AttemptSeq != 1 || ev.EffectiveCostUSD != 1.0 {
		t.Fatalf("事件字段不符: %+v", ev)
	}
}
