package app

import (
	"net/http"
	"testing"
	"time"

	"ccLoad/internal/model"
)

type captureUsagePublisher struct {
	event *model.UsageEvent
}

func (p *captureUsagePublisher) Publish(event *model.UsageEvent) { p.event = event }
func (p *captureUsagePublisher) Close()                          {}

func TestPublishRequestUsageEventCarriesResearchID(t *testing.T) {
	t.Parallel()
	publisher := &captureUsagePublisher{}
	server := &Server{eventPublisher: publisher}
	context, _ := newTestContext(t, newRequest(http.MethodPost, "/v1/messages", nil))
	context.Writer.WriteHeader(http.StatusOK)
	reqCtx := &proxyRequestContext{
		requestID:     "request-1",
		researchID:    "research-1",
		attemptSeq:    1,
		originalModel: "claude",
		startTime:     time.Now(),
	}

	server.publishRequestUsageEvent(context, reqCtx)

	if publisher.event == nil {
		t.Fatal("request usage event was not published")
	}
	if publisher.event.ResearchID != "research-1" || publisher.event.RequestID != "request-1" {
		t.Fatalf("unexpected request usage event: %+v", publisher.event)
	}
}
