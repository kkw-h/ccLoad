package app

import (
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ccLoad/internal/eventbus"
	"ccLoad/internal/model"
)

// newEventPublisherFromEnv 从环境变量构建用量事件发布器。
//
//   - CCLOAD_REDIS 为空 → NoopPublisher（禁用，代理主链路零影响）
//   - CCLOAD_REDIS_EVENT_STREAM / _STREAM_PATTERN / _MODE / _BUFFER 覆盖默认值
//
// DSN 非法时按项目 Fail-Fast 约定 log.Fatal 退出。
func newEventPublisherFromEnv() eventbus.Publisher {
	dsn := strings.TrimSpace(os.Getenv("CCLOAD_REDIS"))
	if dsn == "" {
		return eventbus.NoopPublisher{}
	}

	cfg := eventbus.Config{
		DSN:           dsn,
		Stream:        strings.TrimSpace(os.Getenv("CCLOAD_REDIS_EVENT_STREAM")),
		StreamPattern: strings.TrimSpace(os.Getenv("CCLOAD_REDIS_EVENT_STREAM_PATTERN")),
		Mode:          eventbus.TransportMode(strings.TrimSpace(os.Getenv("CCLOAD_REDIS_EVENT_MODE"))),
	}
	if v := strings.TrimSpace(os.Getenv("CCLOAD_REDIS_EVENT_BUFFER")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.BufferSize = n
		} else {
			log.Fatalf("[FATAL] CCLOAD_REDIS_EVENT_BUFFER 非法（需正整数）: %q", v)
		}
	}

	// 归一化后再记录，日志才反映实际生效的 mode/stream（而非用户留空的原值）。
	cfg = cfg.Normalize()
	pub, err := eventbus.New(cfg)
	if err != nil {
		log.Fatalf("[FATAL] 初始化 Redis 用量事件发布器失败: %v", err)
	}
	log.Printf("[INFO] 用量事件已启用（Redis mode=%s stream=%s stream_pattern=%s）", cfg.Mode, cfg.Stream, cfg.StreamPattern)
	return pub
}

// publishRequestUsageEvent 在请求收尾处发布 request 级瘦汇总事件。
//
// 瘦视图：只带最终 status / 总耗时 / attempt 数等关联字段，不带 usage/cost
// （计费权威由 attempt 事件承载）。attemptSeq==0 表示从未真正尝试上游
// （如无可用渠道），不发事件。
func (s *Server) publishRequestUsageEvent(c *gin.Context, reqCtx *proxyRequestContext) {
	if s.eventPublisher == nil || reqCtx == nil || reqCtx.requestID == "" {
		return
	}
	if reqCtx.attemptSeq == 0 {
		return
	}
	s.eventPublisher.Publish(&model.UsageEvent{
		RequestID:   reqCtx.requestID,
		AttemptSeq:  0,
		Kind:        model.UsageEventRequest,
		Time:        model.JSONTime{Time: time.Now()},
		Environment: reqCtx.tokenEnvironment,
		AuthTokenID: reqCtx.tokenID,
		Model:       reqCtx.originalModel,
		StatusCode:  c.Writer.Status(),
		IsStreaming: reqCtx.isStreaming,
		Duration:    time.Since(reqCtx.startTime).Seconds(),
		ClientIP:    reqCtx.clientIP,
	})
}
