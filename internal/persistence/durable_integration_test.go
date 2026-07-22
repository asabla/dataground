package persistence_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/identity"
	invocationlifecycle "github.com/asabla/dataground/internal/lifecycle/invocation"
	"github.com/asabla/dataground/internal/outbox"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
	"github.com/asabla/dataground/internal/reference"
	"github.com/jackc/pgx/v5"
)

func TestDurablePublicationInvocationAndFencing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	databaseURL := testDatabaseURL(t)
	database, err := persistence.OpenSQL(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.MigrateDownTo(ctx, database, 0); err != nil {
		database.Close()
		t.Fatalf("reset schema: %v", err)
	}
	if err := persistence.MigrateUp(ctx, database); err != nil {
		database.Close()
		t.Fatalf("migrate schema: %v", err)
	}
	database.Close()
	pool, err := persistence.OpenPool(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	repository := persistence.NewRepository(pool)
	domainID := identity.New("iso")
	actorID := "integration-test"
	serviceID := identity.New("svc")
	revisionID := identity.New("rev")

	createService, err := repository.CreateService(ctx, testIdempotency(domainID, "create-service"), persistence.CreateServiceInput{
		ID: serviceID, Name: "durable-test", ActorID: actorID, CorrelationID: identity.New("cor"),
	})
	if err != nil || createService.Status != http.StatusCreated {
		t.Fatalf("create service = (%d, %v)", createService.Status, err)
	}
	replayed, err := repository.CreateService(ctx, testIdempotency(domainID, "create-service"), persistence.CreateServiceInput{
		ID: identity.New("svc"), Name: "ignored-replay", ActorID: actorID, CorrelationID: identity.New("cor"),
	})
	if err != nil || !replayed.Replayed || string(replayed.Body) != string(createService.Body) {
		t.Fatalf("idempotency replay = (%t, %v)", replayed.Replayed, err)
	}
	_, err = repository.CreateRevision(ctx, testIdempotency(domainID, "create-revision"), persistence.CreateRevisionInput{
		ID: revisionID, ServiceID: serviceID, RuntimeProfile: "reference/v1",
		RequiredCapabilities: []string{"tool"}, ActorID: actorID, CorrelationID: identity.New("cor"),
	})
	if err != nil {
		t.Fatal(err)
	}
	publish, err := repository.AcceptPublication(ctx, testIdempotency(domainID, "publish"), persistence.AcceptPublicationInput{
		RevisionID: revisionID, ExpectedVersion: 1, ActorID: actorID,
		CorrelationID: identity.New("cor"), Deadline: time.Now().Add(time.Minute),
	}, reference.Capabilities())
	if err != nil {
		t.Fatal(err)
	}
	var publicationOperation domain.Operation
	if err := json.Unmarshal(publish.Body, &publicationOperation); err != nil {
		t.Fatal(err)
	}

	stale, err := repository.ClaimNext(ctx, persistence.OperationKindPublication, "stale-worker", -time.Second)
	if err != nil || stale == nil {
		t.Fatalf("claim stale lease = (%v, %v)", stale, err)
	}
	if err := repository.Advance(ctx, *stale, "validating", nil); !errors.Is(err, persistence.ErrLeaseLost) {
		t.Fatalf("stale transition = %v, want ErrLeaseLost", err)
	}

	worker := reconcile.New(repository, reconcile.NewReferenceDriver(pool), "replacement-worker")
	runToTerminal(t, ctx, worker, repository, domainID, publicationOperation.Metadata.ID, "published")

	aliasID := identity.New("als")
	_, err = repository.AssignAlias(ctx, testIdempotency(domainID, "assign-alias"), persistence.AssignAliasInput{
		ID: aliasID, ServiceID: serviceID, Name: "stable", RevisionID: revisionID,
		ActorID: actorID, CorrelationID: identity.New("cor"),
	})
	if err != nil {
		t.Fatal(err)
	}
	invocationID := identity.New("inv")
	accepted, err := repository.AcceptInvocation(ctx, testIdempotency(domainID, "invoke"), persistence.AcceptInvocationInput{
		ID: invocationID, ServiceID: serviceID, Alias: "stable", Input: map[string]any{"message": "hello"},
		ActorID: actorID, CorrelationID: identity.New("cor"), Deadline: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	var invocation domain.Invocation
	if err := json.Unmarshal(accepted.Body, &invocation); err != nil {
		t.Fatal(err)
	}
	admissionTarget, err := repository.GetInvocationAdmissionTarget(ctx, domainID, invocation.OperationID)
	if err != nil {
		t.Fatalf("resolve invocation admission target: %v", err)
	}
	if admissionTarget.IsolationDomainID != domainID ||
		admissionTarget.OperationID != invocation.OperationID ||
		admissionTarget.InvocationID != invocationID ||
		admissionTarget.ServiceID != serviceID ||
		admissionTarget.RevisionID != revisionID ||
		admissionTarget.ActorID != actorID ||
		admissionTarget.CorrelationID != invocation.CorrelationID ||
		admissionTarget.StateMachineVersion != 2 {
		t.Fatalf("invocation admission target = %#v", admissionTarget)
	}
	if _, err := repository.GetInvocationAdmissionTarget(ctx, identity.New("iso"), invocation.OperationID); !errors.Is(err, persistence.ErrInvocationAdmissionTargetMissing) {
		t.Fatalf("cross-domain admission target lookup = %v, want a missing target", err)
	}
	if _, err := repository.GetInvocationRuntimeTarget(ctx, domainID, invocation.OperationID); !errors.Is(err, persistence.ErrInvocationRuntimeTargetMissing) {
		t.Fatalf("runtime target before admission = %v, want a missing target", err)
	}
	for _, wantState := range []string{"starting", "running"} {
		ran, err := worker.RunOne(ctx, persistence.OperationKindInvocation)
		if err != nil || !ran {
			t.Fatalf("advance invocation to %s = (%t, %v)", wantState, ran, err)
		}
		operation, err := repository.GetOperation(ctx, domainID, invocation.OperationID)
		if err != nil || operation.ObservedState != wantState {
			t.Fatalf("invocation operation state = (%q, %v), want %q", operation.ObservedState, err, wantState)
		}
	}
	runtimeClaim, err := repository.ClaimNext(
		ctx,
		persistence.OperationKindInvocation,
		"runtime-handoff-probe",
		time.Second,
	)
	if err != nil ||
		runtimeClaim == nil ||
		runtimeClaim.ID != invocation.OperationID ||
		runtimeClaim.ObservedState != "running" ||
		runtimeClaim.LeaseExpiresAt.IsZero() {
		t.Fatalf("claim invocation runtime handoff = (%#v, %v)", runtimeClaim, err)
	}
	runtimeTarget, err := repository.GetClaimedInvocationRuntimeTarget(ctx, *runtimeClaim)
	if err != nil {
		t.Fatalf("resolve claimed invocation runtime target: %v", err)
	}
	if runtimeTarget.IsolationDomainID != domainID ||
		runtimeTarget.OperationID != invocation.OperationID ||
		runtimeTarget.InvocationID != invocationID ||
		runtimeTarget.ServiceID != serviceID ||
		runtimeTarget.RevisionID != revisionID ||
		runtimeTarget.ActorID != actorID ||
		runtimeTarget.CorrelationID != invocation.CorrelationID ||
		runtimeTarget.StateMachineVersion != invocationlifecycle.StateMachineVersion ||
		runtimeTarget.RuntimeProfile != "reference/v1" ||
		runtimeTarget.Input["message"] != "hello" ||
		runtimeTarget.OutputSchema != nil {
		t.Fatalf("invocation runtime target = %#v", runtimeTarget)
	}
	if _, err := repository.GetInvocationRuntimeTarget(ctx, identity.New("iso"), invocation.OperationID); !errors.Is(err, persistence.ErrInvocationRuntimeTargetMissing) {
		t.Fatalf("cross-domain runtime target lookup = %v, want a missing target", err)
	}
	renewedRuntimeClaim, err := repository.RenewLease(ctx, *runtimeClaim, 15*time.Second)
	if err != nil ||
		!renewedRuntimeClaim.LeaseExpiresAt.After(runtimeClaim.LeaseExpiresAt) ||
		renewedRuntimeClaim.LeaseExpiresAt.After(runtimeClaim.DeadlineAt) {
		t.Fatalf(
			"renew invocation runtime claim = (%s, %s, %v)",
			runtimeClaim.LeaseExpiresAt,
			renewedRuntimeClaim.LeaseExpiresAt,
			err,
		)
	}
	staleRuntimeClaim := renewedRuntimeClaim
	staleRuntimeClaim.FencingToken--
	if _, err := repository.GetClaimedInvocationRuntimeTarget(
		ctx,
		staleRuntimeClaim,
	); !errors.Is(err, persistence.ErrLeaseLost) {
		t.Fatalf("stale runtime target claim = %v, want a lost lease", err)
	}
	if _, err := repository.RenewLease(
		ctx,
		staleRuntimeClaim,
		time.Second,
	); !errors.Is(err, persistence.ErrLeaseLost) {
		t.Fatalf("stale runtime lease renewal = %v, want a lost lease", err)
	}
	foreignRuntimeClaim := renewedRuntimeClaim
	foreignRuntimeClaim.IsolationDomainID = identity.New("iso")
	if _, err := repository.GetClaimedInvocationRuntimeTarget(
		ctx,
		foreignRuntimeClaim,
	); !errors.Is(err, persistence.ErrLeaseLost) {
		t.Fatalf("cross-domain runtime target claim = %v, want a lost lease", err)
	}

	runtimeEffect, err := repository.PrepareEffect(
		ctx,
		renewedRuntimeClaim,
		"run-invocation",
		sha256.Sum256([]byte(domainID+":"+invocation.OperationID+":run-invocation")),
	)
	if err != nil {
		t.Fatalf("prepare invocation runtime effect: %v", err)
	}
	runtimeAttempt, err := repository.BeginInvocationRuntimeAttempt(
		ctx,
		renewedRuntimeClaim,
		runtimeEffect,
	)
	if err != nil ||
		runtimeAttempt.IsolationDomainID != domainID ||
		runtimeAttempt.OperationID != invocation.OperationID ||
		runtimeAttempt.EffectID != runtimeEffect.EffectID ||
		runtimeAttempt.LeaseOwner != renewedRuntimeClaim.LeaseOwner ||
		runtimeAttempt.FencingToken != renewedRuntimeClaim.FencingToken ||
		runtimeAttempt.Status != "reserved" ||
		runtimeAttempt.Result != nil {
		t.Fatalf("begin invocation runtime attempt = (%#v, %v)", runtimeAttempt, err)
	}
	if _, err := repository.BeginInvocationRuntimeAttempt(
		ctx,
		renewedRuntimeClaim,
		runtimeEffect,
	); !errors.Is(err, persistence.ErrInvocationRuntimeAttemptAmbiguous) {
		t.Fatalf("repeated invocation runtime attempt = %v, want ambiguous", err)
	}
	if _, err := repository.CompleteInvocationRuntimeAttempt(
		ctx,
		staleRuntimeClaim,
		runtimeEffect,
		map[string]any{"status": "succeeded"},
	); !errors.Is(err, persistence.ErrLeaseLost) {
		t.Fatalf("stale invocation runtime completion = %v, want a lost lease", err)
	}
	runtimeResult := map[string]any{"status": "succeeded", "output": "hello"}
	completedRuntimeAttempt, err := repository.CompleteInvocationRuntimeAttempt(
		ctx,
		renewedRuntimeClaim,
		runtimeEffect,
		runtimeResult,
	)
	if err != nil ||
		completedRuntimeAttempt.Status != "succeeded" ||
		completedRuntimeAttempt.Result["status"] != "succeeded" ||
		completedRuntimeAttempt.Result["output"] != "hello" {
		t.Fatalf("complete invocation runtime attempt = (%#v, %v)", completedRuntimeAttempt, err)
	}
	replayedRuntimeAttempt, err := repository.CompleteInvocationRuntimeAttempt(
		ctx,
		renewedRuntimeClaim,
		runtimeEffect,
		map[string]any{"output": "hello", "status": "succeeded"},
	)
	if err != nil || replayedRuntimeAttempt.Status != "succeeded" {
		t.Fatalf("replay invocation runtime completion = (%#v, %v)", replayedRuntimeAttempt, err)
	}
	if _, err := repository.CompleteInvocationRuntimeAttempt(
		ctx,
		renewedRuntimeClaim,
		runtimeEffect,
		map[string]any{"status": "succeeded", "output": "conflict"},
	); !errors.Is(err, persistence.ErrInvocationRuntimeAttemptConflict) {
		t.Fatalf("conflicting invocation runtime completion = %v, want conflict", err)
	}
	persistedRuntimeAttempt, err := repository.GetInvocationRuntimeAttempt(
		ctx,
		domainID,
		invocation.OperationID,
	)
	if err != nil ||
		persistedRuntimeAttempt.Status != "succeeded" ||
		persistedRuntimeAttempt.Result["output"] != "hello" {
		t.Fatalf("read invocation runtime attempt = (%#v, %v)", persistedRuntimeAttempt, err)
	}
	if _, err := repository.GetInvocationRuntimeAttempt(
		ctx,
		identity.New("iso"),
		invocation.OperationID,
	); !errors.Is(err, persistence.ErrInvocationRuntimeAttemptMissing) {
		t.Fatalf("cross-domain invocation runtime attempt = %v, want missing", err)
	}

	runtimeEvent := persistence.InvocationRuntimeEvent{
		SourceSequence: 1,
		Type:           "output.text.delta",
		Payload:        map[string]any{"text": "hello"},
	}
	persistedRuntimeEvent, err := repository.RecordInvocationRuntimeEvent(
		ctx, renewedRuntimeClaim, runtimeEvent,
	)
	if err != nil {
		t.Fatalf("record invocation runtime event: %v", err)
	}
	replayedRuntimeEvent, err := repository.RecordInvocationRuntimeEvent(
		ctx, renewedRuntimeClaim, runtimeEvent,
	)
	if err != nil || replayedRuntimeEvent.ID != persistedRuntimeEvent.ID ||
		replayedRuntimeEvent.Sequence != persistedRuntimeEvent.Sequence {
		t.Fatalf("replay invocation runtime event = (%#v, %v)", replayedRuntimeEvent, err)
	}
	conflictingRuntimeEvent := runtimeEvent
	conflictingRuntimeEvent.Payload = map[string]any{"text": "conflict"}
	if _, err := repository.RecordInvocationRuntimeEvent(
		ctx, renewedRuntimeClaim, conflictingRuntimeEvent,
	); !errors.Is(err, persistence.ErrInvocationRuntimeEventConflict) {
		t.Fatalf("conflicting invocation runtime event = %v, want conflict", err)
	}
	if _, err := repository.RecordInvocationRuntimeEvent(
		ctx,
		staleRuntimeClaim,
		persistence.InvocationRuntimeEvent{
			SourceSequence: 2,
			Type:           "output.text.delta",
			Payload:        map[string]any{"text": "stale"},
		},
	); !errors.Is(err, persistence.ErrLeaseLost) {
		t.Fatalf("stale invocation runtime event = %v, want a lost lease", err)
	}
	if _, err := repository.RecordInvocationRuntimeEvent(
		ctx,
		foreignRuntimeClaim,
		persistence.InvocationRuntimeEvent{
			SourceSequence: 2,
			Type:           "output.text.delta",
			Payload:        map[string]any{"text": "foreign"},
		},
	); !errors.Is(err, persistence.ErrLeaseLost) {
		t.Fatalf("cross-domain invocation runtime event = %v, want a lost lease", err)
	}
	if _, err := repository.RecordInvocationRuntimeEvent(
		ctx,
		renewedRuntimeClaim,
		persistence.InvocationRuntimeEvent{Type: "output.text.delta", Payload: map[string]any{"text": "invalid"}},
	); !errors.Is(err, persistence.ErrInvocationRuntimeEventInvalid) {
		t.Fatalf("invalid invocation runtime event = %v, want invalid", err)
	}
	events, err := repository.ListEvents(ctx, domainID, invocationID, persistedRuntimeEvent.Sequence-1)
	if err != nil || len(events) != 1 || events[0].ID != persistedRuntimeEvent.ID ||
		events[0].ActorID != actorID || events[0].CorrelationID != invocation.CorrelationID {
		t.Fatalf("persisted invocation runtime events = (%#v, %v)", events, err)
	}
	if err := repository.ScheduleRetry(
		ctx,
		renewedRuntimeClaim,
		"retryable",
		"RUNTIME_HANDOFF_TEST_RELEASE",
		time.Now().UTC(),
	); err != nil {
		t.Fatalf("release invocation runtime test claim: %v", err)
	}

	runToTerminal(t, ctx, worker, repository, domainID, invocation.OperationID, "succeeded")
	if _, err := repository.GetInvocationRuntimeTarget(ctx, domainID, invocation.OperationID); !errors.Is(err, persistence.ErrInvocationRuntimeTargetMissing) {
		t.Fatalf("runtime target after completion = %v, want a missing target", err)
	}
	observed, err := repository.GetInvocation(ctx, domainID, invocationID)
	if err != nil || observed.State != "succeeded" {
		t.Fatalf("invocation state = (%q, %v)", observed.State, err)
	}

	runtimeFailedInvocationID := identity.New("inv")
	runtimeFailedAccepted, err := repository.AcceptInvocation(
		ctx,
		testIdempotency(domainID, "invoke-runtime-failed"),
		persistence.AcceptInvocationInput{
			ID: runtimeFailedInvocationID, ServiceID: serviceID, Alias: "stable",
			Input:   map[string]any{"message": "fail in runtime"},
			ActorID: actorID, CorrelationID: identity.New("cor"),
			Deadline: time.Now().Add(time.Minute),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var runtimeFailedInvocation domain.Invocation
	if err := json.Unmarshal(runtimeFailedAccepted.Body, &runtimeFailedInvocation); err != nil {
		t.Fatal(err)
	}
	for _, wantState := range []string{"starting", "running"} {
		ran, err := worker.RunOne(ctx, persistence.OperationKindInvocation)
		if err != nil || !ran {
			t.Fatalf("advance runtime-failed invocation to %s = (%t, %v)", wantState, ran, err)
		}
		operation, err := repository.GetOperation(
			ctx,
			domainID,
			runtimeFailedInvocation.OperationID,
		)
		if err != nil || operation.ObservedState != wantState {
			t.Fatalf(
				"runtime-failed invocation operation state = (%q, %v), want %q",
				operation.ObservedState,
				err,
				wantState,
			)
		}
	}
	runtimeFailedClaim, err := repository.ClaimNext(
		ctx,
		persistence.OperationKindInvocation,
		"runtime-failure-worker",
		time.Minute,
	)
	if err != nil ||
		runtimeFailedClaim == nil ||
		runtimeFailedClaim.ID != runtimeFailedInvocation.OperationID {
		t.Fatalf("claim runtime-failed invocation = (%#v, %v)", runtimeFailedClaim, err)
	}
	runtimeFailedEffect, err := repository.PrepareEffect(
		ctx,
		*runtimeFailedClaim,
		"run-invocation",
		sha256.Sum256([]byte(domainID+":"+runtimeFailedInvocation.OperationID+":run-invocation")),
	)
	if err != nil {
		t.Fatalf("prepare runtime-failed effect: %v", err)
	}
	if _, err := repository.BeginInvocationRuntimeAttempt(
		ctx,
		*runtimeFailedClaim,
		runtimeFailedEffect,
	); err != nil {
		t.Fatalf("begin runtime-failed attempt: %v", err)
	}
	runtimeFailure := map[string]any{
		"code":   "RUNTIME_TURN_FAILED",
		"status": "failed",
	}
	failedRuntimeAttempt, err := repository.FailInvocationRuntimeAttempt(
		ctx,
		*runtimeFailedClaim,
		runtimeFailedEffect,
		runtimeFailure,
	)
	if err != nil ||
		failedRuntimeAttempt.Status != "failed" ||
		failedRuntimeAttempt.Result["code"] != "RUNTIME_TURN_FAILED" {
		t.Fatalf("fail invocation runtime attempt = (%#v, %v)", failedRuntimeAttempt, err)
	}
	replayedRuntimeFailure, err := repository.FailInvocationRuntimeAttempt(
		ctx,
		*runtimeFailedClaim,
		runtimeFailedEffect,
		map[string]any{"status": "failed", "code": "RUNTIME_TURN_FAILED"},
	)
	if err != nil || replayedRuntimeFailure.Status != "failed" {
		t.Fatalf("replay failed invocation runtime attempt = (%#v, %v)", replayedRuntimeFailure, err)
	}
	if _, err := repository.CompleteInvocationRuntimeAttempt(
		ctx,
		*runtimeFailedClaim,
		runtimeFailedEffect,
		map[string]any{"status": "succeeded"},
	); !errors.Is(err, persistence.ErrInvocationRuntimeAttemptConflict) {
		t.Fatalf("replace failed invocation runtime attempt = %v, want conflict", err)
	}
	persistedRuntimeFailure, err := repository.GetInvocationRuntimeAttempt(
		ctx,
		domainID,
		runtimeFailedInvocation.OperationID,
	)
	if err != nil ||
		persistedRuntimeFailure.Status != "failed" ||
		persistedRuntimeFailure.Result["code"] != "RUNTIME_TURN_FAILED" {
		t.Fatalf("read failed invocation runtime attempt = (%#v, %v)", persistedRuntimeFailure, err)
	}
	if err := repository.RecordEffect(
		ctx,
		runtimeFailedEffect,
		"failed",
		nil,
		"EXTERNAL_EFFECT_TERMINAL_FAILURE",
	); err != nil {
		t.Fatalf("record terminal runtime effect: %v", err)
	}
	if err := repository.Fail(
		ctx,
		*runtimeFailedClaim,
		persistence.OperationFailureRuntime,
	); err != nil {
		t.Fatalf("terminate runtime-failed invocation: %v", err)
	}
	failedInvocation, err := repository.GetInvocation(ctx, domainID, runtimeFailedInvocationID)
	if err != nil || failedInvocation.State != "failed" {
		t.Fatalf("runtime-failed invocation state = (%q, %v)", failedInvocation.State, err)
	}

	rejectedInvocationID := identity.New("inv")
	rejectedAccepted, err := repository.AcceptInvocation(ctx, testIdempotency(domainID, "invoke-rejected"), persistence.AcceptInvocationInput{
		ID: rejectedInvocationID, ServiceID: serviceID, Alias: "stable", Input: map[string]any{"message": "reject"},
		ActorID: actorID, CorrelationID: identity.New("cor"), Deadline: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	var rejectedInvocation domain.Invocation
	if err := json.Unmarshal(rejectedAccepted.Body, &rejectedInvocation); err != nil {
		t.Fatal(err)
	}
	rejectedClaim, err := repository.ClaimNext(ctx, persistence.OperationKindInvocation, "rejection-worker", time.Minute)
	if err != nil || rejectedClaim == nil || rejectedClaim.ID != rejectedInvocation.OperationID {
		t.Fatalf("claim rejected invocation = (%#v, %v)", rejectedClaim, err)
	}
	if err := repository.Advance(ctx, *rejectedClaim, "starting", nil); err != nil {
		t.Fatal(err)
	}
	rejectedClaim, err = repository.ClaimNext(ctx, persistence.OperationKindInvocation, "rejection-worker", time.Minute)
	if err != nil || rejectedClaim == nil || rejectedClaim.ID != rejectedInvocation.OperationID {
		t.Fatalf("reclaim rejected invocation = (%#v, %v)", rejectedClaim, err)
	}
	if err := repository.Fail(ctx, *rejectedClaim, persistence.OperationFailureEffectDenied); err != nil {
		t.Fatal(err)
	}
	rejectedOperation, err := repository.GetOperation(ctx, domainID, rejectedInvocation.OperationID)
	if err != nil || rejectedOperation.ObservedState != "failed" || rejectedOperation.Error == nil ||
		rejectedOperation.Error.Code != "OPERATION_EFFECT_DENIED" || rejectedOperation.Error.Retryable {
		t.Fatalf("rejected invocation operation = (%#v, %v)", rejectedOperation, err)
	}

	cancelledInvocationID := identity.New("inv")
	cancelAccepted, err := repository.AcceptInvocation(ctx, testIdempotency(domainID, "invoke-cancelled"), persistence.AcceptInvocationInput{
		ID: cancelledInvocationID, ServiceID: serviceID, Alias: "stable", Input: map[string]any{"message": "cancel"},
		ActorID: actorID, CorrelationID: identity.New("cor"), Deadline: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	var cancelledInvocation domain.Invocation
	if err := json.Unmarshal(cancelAccepted.Body, &cancelledInvocation); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AcceptCancellation(
		ctx,
		testIdempotency(domainID, "cancel-invalid-principal"),
		persistence.AcceptCancellationInput{InvocationID: cancelledInvocationID},
	); err == nil {
		t.Fatal("cancellation without an effect principal was accepted")
	}
	cancellationActorID := "cancellation-operator"
	cancellationCorrelationID := identity.New("cor")
	if _, err := repository.AcceptCancellation(ctx, testIdempotency(domainID, "cancel"), persistence.AcceptCancellationInput{
		InvocationID:  cancelledInvocationID,
		ActorID:       cancellationActorID,
		CorrelationID: cancellationCorrelationID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AcceptCancellation(ctx, testIdempotency(domainID, "cancel-again"), persistence.AcceptCancellationInput{
		InvocationID:  cancelledInvocationID,
		ActorID:       "different-cancellation-operator",
		CorrelationID: identity.New("cor"),
	}); err != nil {
		t.Fatal(err)
	}
	cancellationTarget, err := repository.GetInvocationCancellationTarget(
		ctx,
		domainID,
		cancelledInvocation.OperationID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if cancellationTarget.IsolationDomainID != domainID ||
		cancellationTarget.OperationID != cancelledInvocation.OperationID ||
		cancellationTarget.InvocationID != cancelledInvocationID ||
		cancellationTarget.ServiceID != serviceID ||
		cancellationTarget.RevisionID != revisionID ||
		cancellationTarget.ActorID != cancellationActorID ||
		cancellationTarget.CorrelationID != cancellationCorrelationID ||
		cancellationTarget.StateMachineVersion != invocationlifecycle.StateMachineVersion ||
		cancellationTarget.AdmissionPrepared {
		t.Fatalf("cancellation target = %#v", cancellationTarget)
	}
	var cancellationOriginalActorID, cancellationOriginalCorrelationID string
	var cancellationEffectActorID, cancellationEffectCorrelationID string
	if err := pool.QueryRow(ctx, `
		SELECT actor_id, correlation_id, effect_actor_id, effect_correlation_id
		FROM invocation_execution_operations
		WHERE isolation_domain_id = $1 AND id = $2
	`, domainID, cancelledInvocation.OperationID).Scan(
		&cancellationOriginalActorID,
		&cancellationOriginalCorrelationID,
		&cancellationEffectActorID,
		&cancellationEffectCorrelationID,
	); err != nil {
		t.Fatal(err)
	}
	if cancellationOriginalActorID != actorID ||
		cancellationOriginalCorrelationID != cancelledInvocation.CorrelationID ||
		cancellationEffectActorID != cancellationActorID ||
		cancellationEffectCorrelationID != cancellationCorrelationID {
		t.Fatalf(
			"cancelled invocation principals = original (%q, %q), effect (%q, %q)",
			cancellationOriginalActorID,
			cancellationOriginalCorrelationID,
			cancellationEffectActorID,
			cancellationEffectCorrelationID,
		)
	}
	cancellationClaim, err := repository.ClaimNext(
		ctx,
		persistence.OperationKindInvocation,
		"cancellation-principal-probe",
		-time.Second,
	)
	if err != nil || cancellationClaim == nil {
		t.Fatalf("claim cancelled invocation = (%v, %v)", cancellationClaim, err)
	}
	if cancellationClaim.ActorID != cancellationActorID ||
		cancellationClaim.CorrelationID != cancellationCorrelationID {
		t.Fatalf(
			"cancelled invocation claim principal = (%q, %q)",
			cancellationClaim.ActorID,
			cancellationClaim.CorrelationID,
		)
	}
	runToTerminal(t, ctx, worker, repository, domainID, cancelledInvocation.OperationID, "cancelled")
	var cancelledEffectPhase, cancelledEffectStatus string
	if err := pool.QueryRow(ctx, `
		SELECT phase, status FROM external_effects
		WHERE isolation_domain_id = $1 AND operation_id = $2
	`, domainID, cancelledInvocation.OperationID).Scan(
		&cancelledEffectPhase,
		&cancelledEffectStatus,
	); err != nil {
		t.Fatal(err)
	}
	if cancelledEffectPhase != "cancel-invocation" || cancelledEffectStatus != "succeeded" {
		t.Fatalf(
			"cancelled invocation effect = (%q, %q), want (cancel-invocation, succeeded)",
			cancelledEffectPhase,
			cancelledEffectStatus,
		)
	}
	if _, err := repository.GetInvocation(ctx, identity.New("iso"), invocationID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross-domain invocation read error = %v, want pgx.ErrNoRows", err)
	}

	repairActorID := "repair-operator"
	repairDeduplicationID := "repair-invocation-dedup-1"
	repairInvocationCorrelationID := identity.New("cor")
	repairInvocationID := identity.New("inv")
	repairAccepted, err := repository.AcceptInvocation(ctx, testIdempotency(domainID, "invoke-expired"), persistence.AcceptInvocationInput{
		ID: repairInvocationID, ServiceID: serviceID, Alias: "stable", Input: map[string]any{"message": "repair"},
		ActorID: actorID, CorrelationID: repairInvocationCorrelationID, Deadline: time.Now().Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	var repairInvocation domain.Invocation
	if err := json.Unmarshal(repairAccepted.Body, &repairInvocation); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.RunOne(ctx, persistence.OperationKindInvocation); err != nil {
		t.Fatal(err)
	}
	failedInvocationOperation, err := repository.GetOperation(ctx, domainID, repairInvocation.OperationID)
	if err != nil || failedInvocationOperation.ObservedState != "failed" {
		t.Fatalf("expired invocation operation = (%q, %v), want failed", failedInvocationOperation.ObservedState, err)
	}
	repairInvocationDeadline := time.Now().UTC().Add(time.Minute)
	if err := repository.RepairOperation(
		ctx, persistence.OperationKindInvocation, domainID, repairInvocation.OperationID,
		repairActorID, "provider recovered", repairDeduplicationID, repairInvocationDeadline,
	); err != nil {
		t.Fatal(err)
	}
	if err := repository.RepairOperation(
		ctx, persistence.OperationKindInvocation, domainID, repairInvocation.OperationID,
		repairActorID, "provider recovered", repairDeduplicationID, repairInvocationDeadline,
	); err != nil {
		t.Fatalf("repeat invocation repair: %v", err)
	}
	if err := repository.RepairOperation(
		ctx, persistence.OperationKindInvocation, domainID, repairInvocation.OperationID,
		"different-repair-operator", "provider recovered", repairDeduplicationID, repairInvocationDeadline,
	); err == nil {
		t.Fatal("repair deduplication actor conflict was accepted")
	}
	repairTarget, err := repository.GetInvocationAdmissionTarget(ctx, domainID, repairInvocation.OperationID)
	if err != nil {
		t.Fatalf("resolve repaired invocation admission target: %v", err)
	}
	if repairTarget.ActorID != repairActorID || repairTarget.CorrelationID != repairDeduplicationID {
		t.Fatalf("repaired invocation admission principal = (%q, %q)", repairTarget.ActorID, repairTarget.CorrelationID)
	}
	var originalActorID, originalCorrelationID, effectActorID, effectCorrelationID string
	if err := pool.QueryRow(ctx, `
		SELECT actor_id, correlation_id, effect_actor_id, effect_correlation_id
		FROM invocation_execution_operations
		WHERE isolation_domain_id = $1 AND id = $2
	`, domainID, repairInvocation.OperationID).Scan(
		&originalActorID, &originalCorrelationID, &effectActorID, &effectCorrelationID,
	); err != nil {
		t.Fatal(err)
	}
	if originalActorID != actorID || originalCorrelationID != repairInvocationCorrelationID ||
		effectActorID != repairActorID || effectCorrelationID != repairDeduplicationID {
		t.Fatalf(
			"repaired invocation principals = original (%q, %q), effect (%q, %q)",
			originalActorID, originalCorrelationID, effectActorID, effectCorrelationID,
		)
	}
	repairClaim, err := repository.ClaimNext(ctx, persistence.OperationKindInvocation, "repair-principal-probe", -time.Second)
	if err != nil || repairClaim == nil {
		t.Fatalf("claim repaired invocation = (%v, %v)", repairClaim, err)
	}
	if repairClaim.ActorID != repairActorID || repairClaim.CorrelationID != repairDeduplicationID {
		t.Fatalf("repaired invocation claim principal = (%q, %q)", repairClaim.ActorID, repairClaim.CorrelationID)
	}
	runToTerminal(t, ctx, worker, repository, domainID, repairInvocation.OperationID, "succeeded")

	repairRevisionID := identity.New("rev")
	if _, err := repository.CreateRevision(ctx, testIdempotency(domainID, "create-repair-revision"), persistence.CreateRevisionInput{
		ID: repairRevisionID, ServiceID: serviceID, RuntimeProfile: "reference/v1",
		ActorID: actorID, CorrelationID: identity.New("cor"),
	}); err != nil {
		t.Fatal(err)
	}
	repairPublication, err := repository.AcceptPublication(ctx, testIdempotency(domainID, "publish-expired"), persistence.AcceptPublicationInput{
		RevisionID: repairRevisionID, ExpectedVersion: 1, ActorID: actorID,
		CorrelationID: identity.New("cor"), Deadline: time.Now().Add(-time.Second),
	}, reference.Capabilities())
	if err != nil {
		t.Fatal(err)
	}
	var repairOperation domain.Operation
	if err := json.Unmarshal(repairPublication.Body, &repairOperation); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.RunOne(ctx, persistence.OperationKindPublication); err != nil {
		t.Fatal(err)
	}
	failedOperation, err := repository.GetOperation(ctx, domainID, repairOperation.Metadata.ID)
	if err != nil || failedOperation.ObservedState != "failed" {
		t.Fatalf("expired operation = (%q, %v), want failed", failedOperation.ObservedState, err)
	}
	newDeadline := time.Now().UTC().Add(time.Minute)
	if err := repository.RepairOperation(
		ctx, persistence.OperationKindPublication, domainID, repairOperation.Metadata.ID,
		actorID, "injected recovery", "repair-dedup-1", newDeadline,
	); err != nil {
		t.Fatal(err)
	}
	if err := repository.RepairOperation(
		ctx, persistence.OperationKindPublication, domainID, repairOperation.Metadata.ID,
		actorID, "injected recovery", "repair-dedup-1", newDeadline,
	); err != nil {
		t.Fatalf("repeat repair: %v", err)
	}
	if err := repository.RepairOperation(
		ctx, persistence.OperationKindPublication, domainID, repairOperation.Metadata.ID,
		actorID, "different content", "repair-dedup-1", newDeadline,
	); err == nil {
		t.Fatal("repair deduplication conflict was accepted")
	}
	runToTerminal(t, ctx, worker, repository, domainID, repairOperation.Metadata.ID, "published")

	callbackDigest := sha256.Sum256([]byte("callback-payload"))
	callbackTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	replayedCallback, err := persistence.RecordInbox(
		ctx, callbackTx, domainID, persistence.InboxCallback, "callback-1",
		callbackDigest[:], map[string]any{"accepted": true}, time.Now().UTC(),
	)
	if err != nil || replayedCallback {
		t.Fatalf("first callback inbox = (%v, %v)", replayedCallback, err)
	}
	if err := callbackTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	callbackTx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	replayedCallback, err = persistence.RecordInbox(
		ctx, callbackTx, domainID, persistence.InboxCallback, "callback-1",
		callbackDigest[:], map[string]any{"accepted": true}, time.Now().UTC(),
	)
	if err != nil || !replayedCallback {
		t.Fatalf("callback replay = (%v, %v)", replayedCallback, err)
	}
	if err := callbackTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	var transitionEvents int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM outbox_events
		WHERE isolation_domain_id = $1 AND aggregate_id IN ($2, $3)
	`, domainID, publicationOperation.Metadata.ID, invocation.OperationID).Scan(&transitionEvents); err != nil {
		t.Fatal(err)
	}
	if transitionEvents < 7 {
		t.Fatalf("transition outbox events = %d, want at least 7", transitionEvents)
	}
	dispatcher := outbox.New(repository, outbox.AcknowledgePublisher{}, "outbox-worker")
	for attempt := 0; attempt < 100; attempt++ {
		ran, dispatchErr := dispatcher.RunOne(ctx)
		if dispatchErr != nil {
			t.Fatal(dispatchErr)
		}
		if !ran {
			break
		}
	}
	var pendingEvents int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE status = 'pending'`).Scan(&pendingEvents); err != nil {
		t.Fatal(err)
	}
	if pendingEvents != 0 {
		t.Fatalf("pending outbox events = %d, want zero", pendingEvents)
	}
}

func runToTerminal(
	t *testing.T,
	ctx context.Context,
	worker *reconcile.Reconciler,
	repository *persistence.Repository,
	domainID string,
	operationID string,
	terminal string,
) {
	t.Helper()
	for attempt := 0; attempt < 12; attempt++ {
		operation, err := repository.GetOperation(ctx, domainID, operationID)
		if err != nil {
			t.Fatal(err)
		}
		if operation.ObservedState == terminal {
			return
		}
		if _, err := worker.RunOne(ctx, operation.Kind); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatalf("operation %s did not reach %s", operationID, terminal)
}

func testIdempotency(domainID, key string) persistence.Idempotency {
	digest := sha256.Sum256([]byte(key))
	return persistence.Idempotency{
		IsolationDomainID: domainID, Method: http.MethodPost, Path: "/integration/" + key,
		Key: "test-" + key, RequestDigest: digest,
	}
}

func testDatabaseURL(t *testing.T) string {
	t.Helper()
	databaseURL := os.Getenv("DATAGROUND_TEST_DATABASE_URL")
	if databaseURL == "" {
		if os.Getenv("DATAGROUND_REQUIRE_TEST_DATABASE") == "true" {
			t.Fatal("DATAGROUND_TEST_DATABASE_URL is required")
		}
		t.Skip("DATAGROUND_TEST_DATABASE_URL is not set")
	}
	return databaseURL
}
