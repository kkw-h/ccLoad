package eventbus

import (
	"context"
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/redis/go-redis/v9"

	"ccLoad/internal/model"
)

// redisSink 将用量事件序列化为 JSON 后写入 Redis。
type redisSink struct {
	client        *redis.Client
	stream        string
	streamPattern string
	mode          TransportMode
}

func newRedisSink(cfg Config) (*redisSink, error) {
	opt, err := redis.ParseURL(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("解析 CCLOAD_REDIS DSN 失败: %w", err)
	}
	return &redisSink{
		client:        redis.NewClient(opt),
		stream:        cfg.Stream,
		streamPattern: cfg.StreamPattern,
		mode:          cfg.Mode,
	}, nil
}

func (s *redisSink) send(ctx context.Context, ev *model.UsageEvent) error {
	payload, err := sonic.Marshal(ev)
	if err != nil {
		return fmt.Errorf("序列化用量事件失败: %w", err)
	}
	switch s.mode {
	case ModePubSub:
		return s.client.Publish(ctx, s.streamName(ev), payload).Err()
	default:
		return s.client.XAdd(ctx, &redis.XAddArgs{
			Stream: s.streamName(ev),
			Values: map[string]any{"data": payload},
		}).Err()
	}
}

func (s *redisSink) streamName(ev *model.UsageEvent) string {
	if ev == nil || ev.Environment == "" || s.streamPattern == "" {
		return s.stream
	}
	if !strings.Contains(s.streamPattern, "{env}") {
		return s.stream
	}
	return strings.ReplaceAll(s.streamPattern, "{env}", ev.Environment)
}

func (s *redisSink) Close() error {
	return s.client.Close()
}
