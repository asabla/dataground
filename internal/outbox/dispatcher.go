package outbox

import (
	"context"
	"time"

	"github.com/asabla/dataground/internal/persistence"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

type Store interface {
	ClaimOutbox(context.Context, string, time.Duration) (*persistence.OutboxClaim, error)
	CompleteOutbox(context.Context, persistence.OutboxClaim) error
	RetryOutbox(context.Context, persistence.OutboxClaim, time.Time) error
}

type Publisher interface {
	Publish(context.Context, persistence.OutboxClaim) error
}

type Dispatcher struct {
	store    Store
	publish  Publisher
	workerID string
	now      func() time.Time
}

func New(store Store, publisher Publisher, workerID string) *Dispatcher {
	return &Dispatcher{store: store, publish: publisher, workerID: workerID, now: func() time.Time { return time.Now().UTC() }}
}

func (dispatcher *Dispatcher) RunOne(ctx context.Context) (bool, error) {
	ctx, span := otel.Tracer("dataground/outbox-dispatcher").Start(ctx, "outbox.deliver")
	defer span.End()
	claim, err := dispatcher.store.ClaimOutbox(ctx, dispatcher.workerID, 30*time.Second)
	if err != nil || claim == nil {
		return false, err
	}
	span.SetAttributes(
		attribute.String("dataground.outbox.id", claim.ID),
		attribute.String("dataground.isolation_domain.id", claim.IsolationDomainID),
		attribute.String("dataground.event.type", claim.EventType),
	)
	if err := dispatcher.publish.Publish(ctx, *claim); err != nil {
		delay := time.Duration(1<<min(claim.Attempt, 6)) * time.Second
		return true, dispatcher.store.RetryOutbox(ctx, *claim, dispatcher.now().Add(delay))
	}
	return true, dispatcher.store.CompleteOutbox(ctx, *claim)
}

type AcknowledgePublisher struct{}

func (AcknowledgePublisher) Publish(context.Context, persistence.OutboxClaim) error { return nil }
