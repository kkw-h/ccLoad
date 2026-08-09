package eventbus

import (
	"context"
	"sync"
	"testing"
	"time"

	"ccLoad/internal/model"
)

// fakeSink 记录收到的事件，可选阻塞以模拟慢投递。
type fakeSink struct {
	mu      sync.Mutex
	events  []*model.UsageEvent
	release chan struct{} // 非 nil 时 send 阻塞直到收到信号
	closed  bool
}

func (f *fakeSink) send(_ context.Context, ev *model.UsageEvent) error {
	if f.release != nil {
		<-f.release
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
	return nil
}

func (f *fakeSink) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeSink) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events)
}

func TestNoopPublisher(t *testing.T) {
	var p Publisher = NoopPublisher{}
	p.Publish(&model.UsageEvent{RequestID: "x"})
	p.Publish(nil)
	p.Close() // 不 panic 即可
}

func TestAsyncPublisher_DeliversAndDrainsOnClose(t *testing.T) {
	sink := &fakeSink{}
	p := newAsyncPublisher(sink, 16)

	const n = 10
	for i := 0; i < n; i++ {
		p.Publish(&model.UsageEvent{RequestID: "r", AttemptSeq: i})
	}
	// Close 应排空在途事件后再返回。
	p.Close()

	if got := sink.count(); got != n {
		t.Fatalf("投递事件数=%d, 期望 %d", got, n)
	}
	if !sink.closed {
		t.Fatal("Close 未关闭底层 sink")
	}
}

func TestAsyncPublisher_NilAndClosedAreSafe(t *testing.T) {
	sink := &fakeSink{}
	p := newAsyncPublisher(sink, 4)
	p.Publish(nil) // 忽略
	p.Close()
	p.Close() // 幂等，不 panic
}

func TestAsyncPublisher_DropsWhenQueueFull(t *testing.T) {
	// sink 阻塞在第一条上，队列容量 2：后续 Publish 应被丢弃而非阻塞。
	release := make(chan struct{})
	sink := &fakeSink{release: release}
	p := newAsyncPublisher(sink, 2)

	// 第 1 条会被 worker 取走并卡在 send；再灌满队列（容量 2）后继续 Publish 必然丢弃。
	for i := 0; i < 50; i++ {
		p.Publish(&model.UsageEvent{RequestID: "r", AttemptSeq: i})
	}

	// 给 worker 一点时间取走第一条。
	deadline := time.Now().Add(time.Second)
	for p.dropCount.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if p.dropCount.Load() == 0 {
		t.Fatal("队列满时应有事件被丢弃并计数")
	}

	close(release) // 放行，允许 Close 正常排空
	p.Close()
}
