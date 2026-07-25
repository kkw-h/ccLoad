package eventbus

import (
	"context"
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/redis/go-redis/v9"

	"ccLoad/internal/model"
)

// redisSink 将用量事件序列化为 JSON 后写入 Redis。
type redisSink struct {
	client *redis.Client
	stream string
	mode   TransportMode
}

func newRedisSink(cfg Config) (*redisSink, error) {
	opt, err := redis.ParseURL(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("解析 CCLOAD_REDIS DSN 失败: %w", err)
	}
	return &redisSink{
		client: redis.NewClient(opt),
		stream: cfg.Stream,
		mode:   cfg.Mode,
	}, nil
}

func (s *redisSink) send(ctx context.Context, ev *model.UsageEvent) error {
	payload, err := sonic.Marshal(ev)
	if err != nil {
		return fmt.Errorf("序列化用量事件失败: %w", err)
	}
	switch s.mode {
	case ModePubSub:
		return s.client.Publish(ctx, s.stream, payload).Err()
	default:
		return s.client.XAdd(ctx, &redis.XAddArgs{
			Stream: s.stream,
			Values: map[string]any{"data": payload},
		}).Err()
	}
}

func (s *redisSink) Close() error {
	return s.client.Close()
}
