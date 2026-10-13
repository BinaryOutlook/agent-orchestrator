package telemetry

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// OptOutSink drops events while the user has telemetry switched off. It sits in
// front of the aggregator so opted-out activity never enters a rollup bucket
// that a later opt-in would flush to the remote.
type OptOutSink struct {
	next     ports.EventSink
	optedOut func() bool
}

// NewOptOutSink wraps next. A nil check returns next unchanged.
func NewOptOutSink(next ports.EventSink, optedOut func() bool) ports.EventSink {
	if next == nil || optedOut == nil {
		return next
	}
	return &OptOutSink{next: next, optedOut: optedOut}
}

// Emit forwards the event unless telemetry is currently opted out.
func (s *OptOutSink) Emit(ctx context.Context, ev ports.TelemetryEvent) {
	if s.optedOut() {
		return
	}
	s.next.Emit(ctx, ev)
}

// Close delegates so the wrapped sink still flushes on shutdown.
func (s *OptOutSink) Close(ctx context.Context) error { return s.next.Close(ctx) }
