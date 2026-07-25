// Package eventbus 向外部事件总线（Redis）发布 ccLoad 用量事件。
//
// 设计要点：
//   - Fail-open：未配置或后端不可用时装配 NoopPublisher，代理主链路零影响。
//   - 不阻塞请求：Publish 非阻塞入队，独立 worker 消费；队列满则丢弃并计数。
//   - 事件来源单一：调用方传入已归一化的 *model.UsageEvent，本包只负责序列化与投递。
package eventbus

import (
	"strings"

	"ccLoad/internal/model"
)

// Publisher 是用量事件发布器。实现必须是并发安全的。
type Publisher interface {
	// Publish 非阻塞投递一条用量事件。ev 为 nil 时忽略。
	Publish(ev *model.UsageEvent)
	// Close 停止发布并尽力排空在途事件（用于优雅关闭）。幂等。
	Close()
}

// NoopPublisher 在未配置事件总线时使用，所有操作均为空。
type NoopPublisher struct{}

// Publish 实现 Publisher，空操作。
func (NoopPublisher) Publish(*model.UsageEvent) {}

// Close 实现 Publisher，空操作。
func (NoopPublisher) Close() {}

// TransportMode 决定 Redis 投递方式。
type TransportMode string

const (
	// ModeStream 使用 XADD 写入 Redis Stream（默认，可回溯 / 消费组）。
	ModeStream TransportMode = "stream"
	// ModePubSub 使用 PUBLISH 发布到频道（无持久化）。
	ModePubSub TransportMode = "pubsub"
)

// Config 描述事件总线配置，通常从环境变量解析。
type Config struct {
	DSN        string        // Redis DSN；空表示禁用（装配 NoopPublisher）
	Stream     string        // Stream / 频道名
	Mode       TransportMode // stream / pubsub
	BufferSize int           // 事件队列容量
}

// 默认值。
const (
	DefaultStream     = "ccload:usage"
	DefaultBufferSize = 1024
)

// normalize 填充缺省值并归一化字段。
func (c Config) normalize() Config {
	c.DSN = strings.TrimSpace(c.DSN)
	if c.Stream == "" {
		c.Stream = DefaultStream
	}
	switch c.Mode {
	case ModePubSub:
		// keep
	default:
		c.Mode = ModeStream
	}
	if c.BufferSize <= 0 {
		c.BufferSize = DefaultBufferSize
	}
	return c
}

// New 按配置构建 Publisher。
//
// DSN 为空时返回 NoopPublisher（禁用）。构建 Redis 连接失败（DSN 非法）
// 返回错误，交由调用方 Fail-Fast。
func New(cfg Config) (Publisher, error) {
	cfg = cfg.normalize()
	if cfg.DSN == "" {
		return NoopPublisher{}, nil
	}
	sink, err := newRedisSink(cfg)
	if err != nil {
		return nil, err
	}
	return newAsyncPublisher(sink, cfg.BufferSize), nil
}
