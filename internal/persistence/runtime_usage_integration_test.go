package persistence_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
)

func TestRuntimeUsageIsAtomicScopedReplayableAndSourceOrdered(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newRuntimeQuestionFixture(t, ctx)
	repository := fixture.repository
	scope, id := fixture.target.IsolationDomainID, fixture.target.InvocationID
	read := func() domain.Invocation {
		t.Helper()
		value, err := repository.GetInvocation(ctx, scope, id)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := read()
	if before.Usage != nil {
		t.Fatal("missing usage was replaced with zero")
	}
	snapshot := func(sequence uint64, input, output, total int) persistence.InvocationRuntimeEvent {
		return persistence.InvocationRuntimeEvent{SourceSequence: sequence, Type: "usage.recorded", Payload: domain.Usage{InputTokens: input, OutputTokens: output, TotalTokens: total}.SnapshotPayload()}
	}
	record := func(event persistence.InvocationRuntimeEvent) domain.EventEnvelope {
		t.Helper()
		value, err := repository.RecordInvocationRuntimeEvent(ctx, fixture.claim, event)
		if err != nil {
			t.Fatal(err)
		}
		if value.IsolationDomainID != scope || value.InvocationID != id || value.CorrelationID != fixture.target.CorrelationID || value.Source != "runtime" {
			t.Fatal("usage journal lost scope or attribution")
		}
		return value
	}
	assertUsage := func(want domain.Usage, version int) {
		t.Helper()
		value := read()
		if value.Usage == nil || *value.Usage != want || value.Metadata.Version != version {
			t.Fatalf("usage/version = %#v / %d, want %#v / %d", value.Usage, value.Metadata.Version, want, version)
		}
	}
	first := snapshot(10, 12, 8, 20)
	firstEvent := record(first)
	version := before.Metadata.Version + 1
	assertUsage(domain.Usage{InputTokens: 12, OutputTokens: 8, TotalTokens: 20}, version)
	// Recreate the repository to prove replay/projection does not depend on process state.
	repository = persistence.NewRepository(fixture.pool)
	replay := record(first)
	if replay.ID != firstEvent.ID || replay.Sequence != firstEvent.Sequence {
		t.Fatal("replay appended another usage event")
	}
	latest := snapshot(20, 22, 18, 40)
	record(latest)
	version++
	assertUsage(domain.Usage{InputTokens: 22, OutputTokens: 18, TotalTokens: 40}, version)
	record(snapshot(15, 18, 12, 30))
	record(first)
	assertUsage(domain.Usage{InputTokens: 22, OutputTokens: 18, TotalTokens: 40}, version)
	if _, err := repository.RecordInvocationRuntimeEvent(ctx, fixture.claim, snapshot(20, 23, 18, 41)); !errors.Is(err, persistence.ErrInvocationRuntimeEventConflict) {
		t.Fatal("changed replay was not rejected", err)
	}
	record(snapshot(30, 10, 6, 16))
	version++
	record(snapshot(40, 10, 6, 16))
	want := domain.Usage{InputTokens: 10, OutputTokens: 6, TotalTokens: 16}
	assertUsage(want, version)
	for _, payload := range []map[string]any{
		{}, {"inputTokens": 1, "outputTokens": 2}, {"inputTokens": nil, "outputTokens": 2, "totalTokens": 3},
		{"inputTokens": -1, "outputTokens": 2, "totalTokens": 1}, {"inputTokens": 1.5, "outputTokens": 2, "totalTokens": 3},
		{"inputTokens": 1, "outputTokens": 2, "totalTokens": int64(9007199254740992)},
		{"inputTokens": 1, "outputTokens": 2, "totalTokens": 3, "nativeThread": "private"},
	} {
		_, err := repository.RecordInvocationRuntimeEvent(ctx, fixture.claim, persistence.InvocationRuntimeEvent{SourceSequence: 50, Type: "usage.recorded", Payload: payload})
		if !errors.Is(err, persistence.ErrInvocationRuntimeEventInvalid) {
			t.Fatal("invalid usage was admitted", err)
		}
	}
	stale := fixture.claim
	stale.FencingToken++
	foreign := fixture.claim
	foreign.IsolationDomainID = identity.New("iso")
	for _, claim := range []persistence.OperationClaim{stale, foreign} {
		if _, err := repository.RecordInvocationRuntimeEvent(ctx, claim, snapshot(50, 1, 2, 3)); !errors.Is(err, persistence.ErrLeaseLost) {
			t.Fatal("unowned claim changed usage", err)
		}
	}
	assertUsage(want, version)
	eventsBefore, err := repository.ListEvents(ctx, scope, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `CREATE FUNCTION reject_usage_projection() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test usage projection rejection'; END $$;
 CREATE TRIGGER reject_usage_projection BEFORE UPDATE OF usage ON invocations FOR EACH ROW EXECUTE FUNCTION reject_usage_projection()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := fixture.pool.Exec(context.Background(), `DROP TRIGGER IF EXISTS reject_usage_projection ON invocations; DROP FUNCTION IF EXISTS reject_usage_projection()`); err != nil {
			t.Error(err)
		}
	})
	if _, err := repository.RecordInvocationRuntimeEvent(ctx, fixture.claim, snapshot(50, 1, 2, 3)); err == nil {
		t.Fatal("projection failure was ignored")
	}
	if _, err := fixture.pool.Exec(ctx, `DROP TRIGGER reject_usage_projection ON invocations; DROP FUNCTION reject_usage_projection()`); err != nil {
		t.Fatal(err)
	}
	eventsAfter, err := repository.ListEvents(ctx, scope, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(eventsBefore, eventsAfter) {
		t.Fatal("projection failure left a partial journal write")
	}
	assertUsage(want, version)
	// The failed source sequence remains available after rollback.
	record(snapshot(50, 1, 2, 3))
	version++
	assertUsage(domain.Usage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3}, version)
	if _, err := repository.AcceptCancellation(ctx, testIdempotency(scope, "usage-cancel"), persistence.AcceptCancellationInput{InvocationID: id, ActorID: "requester", CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	cancelled := read()
	if _, err := repository.RecordInvocationRuntimeEvent(ctx, fixture.claim, snapshot(60, 9, 9, 18)); !errors.Is(err, persistence.ErrLeaseLost) {
		t.Fatal("cancelled invocation accepted usage", err)
	}
	assertUsage(domain.Usage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3}, cancelled.Metadata.Version)
	encoded, err := json.Marshal(read())
	if err != nil {
		t.Fatal(err)
	}
	var public domain.Invocation
	if json.Unmarshal(encoded, &public) != nil || public.Usage == nil || public.Usage.TotalTokens != 3 {
		t.Fatal("durable usage is absent from the invocation response")
	}
}
