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
	if _, err := repository.GetInvocationAdmissionTarget(ctx, identity.New("iso"), invocation.OperationID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross-domain admission target lookup = %v, want no rows", err)
	}
	runToTerminal(t, ctx, worker, repository, domainID, invocation.OperationID, "succeeded")
	observed, err := repository.GetInvocation(ctx, domainID, invocationID)
	if err != nil || observed.State != "succeeded" {
		t.Fatalf("invocation state = (%q, %v)", observed.State, err)
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
	if _, err := repository.AcceptCancellation(ctx, testIdempotency(domainID, "cancel"), persistence.AcceptCancellationInput{
		InvocationID: cancelledInvocationID, ActorID: actorID, CorrelationID: identity.New("cor"),
	}); err != nil {
		t.Fatal(err)
	}
	runToTerminal(t, ctx, worker, repository, domainID, cancelledInvocation.OperationID, "cancelled")
	var cancelledEffects int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM external_effects
		WHERE isolation_domain_id = $1 AND operation_id = $2
	`, domainID, cancelledInvocation.OperationID).Scan(&cancelledEffects); err != nil {
		t.Fatal(err)
	}
	if cancelledEffects != 0 {
		t.Fatalf("cancelled queued invocation created %d external effects, want zero", cancelledEffects)
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
