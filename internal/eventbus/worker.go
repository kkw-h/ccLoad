package eventbus

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"ccLoad/internal/model"
)

// sink 是事件的实际投递目标（Redis Stream / PubSub）。
type sink interface {
	send(ctx context.Context, ev *model.UsageEvent) error
	Close() error
}

// asyncPublisher 用有界队列 + 单 worker 解耦请求链路与投递。
// 队列满时丢弃事件并计数（Fail-open：绝不阻塞请求）。
type asyncPublisher struct {
	sink      sink
	queue     chan *model.UsageEvent
	closeOnce sync.Once
	done      chan struct{} // worker 退出信号
	stop      chan struct{} // 通知 worker 停止

	dropCount atomic.Int64
}

func newAsyncPublisher(s sink, bufferSize int) *asyncPublisher {
	p := &asyncPublisher{
		sink:  s,
		queue: make(chan *model.UsageEvent, bufferSize),
		done:  make(chan struct{}),
		stop:  make(chan struct{}),
	}
	go p.worker()
	return p
}

// Publish 非阻塞入队；队列满则丢弃并采样告警。
func (p *asyncPublisher) Publish(ev *model.UsageEvent) {
	if ev == nil {
		return
	}
	select {
	case p.queue <- ev:
	default:
		count := p.dropCount.Add(1)
		if count%100 == 1 {
			log.Printf("[WARN] 用量事件队列已满，事件被丢弃 (累计: %d) - 考虑增大 CCLOAD_REDIS_EVENT_BUFFER", count)
		}
	}
}

func (p *asyncPublisher) worker() {
	defer close(p.done)
	for {
		select {
		case <-p.stop:
			p.drain()
			return
		case ev := <-p.queue:
			p.deliver(ev)
		}
	}
}

// drain 在关闭时尽力排空剩余事件（非阻塞读，直到队列空）。
func (p *asyncPublisher) drain() {
	for {
		select {
		case ev := <-p.queue:
			p.deliver(ev)
		default:
			return
		}
	}
}

func (p *asyncPublisher) deliver(ev *model.UsageEvent) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := p.sink.send(ctx, ev); err != nil {
		count := p.dropCount.Add(1)
		if count%100 == 1 {
			log.Printf("[WARN] 用量事件投递失败 (累计: %d): %v", count, err)
		}
	}
}

// Close 停止 worker、排空在途事件并关闭底层连接。幂等。
func (p *asyncPublisher) Close() {
	p.closeOnce.Do(func() {
		close(p.stop)
		<-p.done
		_ = p.sink.Close()
	})
}
