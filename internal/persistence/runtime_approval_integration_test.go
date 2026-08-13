package persistence_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
	"github.com/asabla/dataground/internal/reference"
)

func TestInvocationRuntimeApprovalLifecycleIsDurableAndSingleUse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	databaseURL := testDatabaseURL(t)
	database, err := persistence.OpenSQL(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.MigrateDownTo(ctx, database, 0); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := persistence.MigrateUp(ctx, database); err != nil {
		database.Close()
		t.Fatal(err)
	}
	database.Close()
	pool, err := persistence.OpenPool(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	repository := persistence.NewRepository(pool)
	domainID, serviceID, revisionID := identity.New("iso"), identity.New("svc"), identity.New("rev")
	if result, err := repository.CreateService(
		ctx,
		testIdempotency(domainID, "approval-service"),
		persistence.CreateServiceInput{
			ID: serviceID, Name: "approval-service", ActorID: "creator",
			CorrelationID: identity.New("cor"),
		},
	); err != nil || result.Status != http.StatusCreated {
		t.Fatalf("create service = (%d, %v)", result.Status, err)
	}
	if _, err := repository.CreateRevision(
		ctx,
		testIdempotency(domainID, "approval-revision"),
		persistence.CreateRevisionInput{
			ID: revisionID, ServiceID: serviceID, RuntimeProfile: "reference/v1",
			RequiredCapabilities: []string{"tool"}, ActorID: "creator",
			CorrelationID: identity.New("cor"),
		},
	); err != nil {
		t.Fatal(err)
	}
	published, err := repository.AcceptPublication(
		ctx,
		testIdempotency(domainID, "approval-publish"),
		persistence.AcceptPublicationInput{
			RevisionID: revisionID, ExpectedVersion: 1, ActorID: "publisher",
			CorrelationID: identity.New("cor"), Deadline: time.Now().Add(time.Minute),
		},
		reference.Capabilities(),
	)
	if err != nil {
		t.Fatal(err)
	}
	var publication domain.Operation
	if err := json.Unmarshal(published.Body, &publication); err != nil {
		t.Fatal(err)
	}
	worker := reconcile.New(repository, reconcile.NewReferenceDriver(pool), "approval-setup")
	runToTerminal(t, ctx, worker, repository, domainID, publication.Metadata.ID, "published")
	if _, err := repository.AssignAlias(
		ctx,
		testIdempotency(domainID, "approval-alias"),
		persistence.AssignAliasInput{
			ID: identity.New("als"), ServiceID: serviceID, Name: "stable",
			RevisionID: revisionID, ActorID: "publisher",
			CorrelationID: identity.New("cor"),
		},
	); err != nil {
		t.Fatal(err)
	}
	invocationID := identity.New("inv")
	accepted, err := repository.AcceptInvocation(
		ctx,
		testIdempotency(domainID, "approval-invocation"),
		persistence.AcceptInvocationInput{
			ID: invocationID, ServiceID: serviceID, Alias: "stable",
			Input: map[string]any{"prompt": "change a file"}, ActorID: "requester",
			CorrelationID: identity.New("cor"), Deadline: time.Now().Add(time.Minute),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var invocation domain.Invocation
	if err := json.Unmarshal(accepted.Body, &invocation); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		if ran, err := worker.RunOne(ctx, persistence.OperationKindInvocation); err != nil || !ran {
			t.Fatalf("advance invocation = (%t, %v)", ran, err)
		}
	}
	claim, err := repository.ClaimNext(
		ctx, persistence.OperationKindInvocation, "approval-runtime", time.Minute,
	)
	if err != nil || claim == nil {
		t.Fatalf("claim runtime = (%#v, %v)", claim, err)
	}
	target, err := repository.GetClaimedInvocationRuntimeTarget(ctx, *claim)
	if err != nil {
		t.Fatal(err)
	}
	effect, err := repository.PrepareEffect(
		ctx,
		*claim,
		"run-invocation",
		sha256.Sum256([]byte(domainID+":"+invocation.OperationID+":approval-runtime")),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.BeginInvocationRuntimeAttempt(ctx, *claim, effect); err != nil {
		t.Fatal(err)
	}
	request := persistence.InvocationRuntimeApprovalRequest{
		SourceSequence: 7, RequestedAction: "workspace.change",
	}
	approval, err := repository.RecordInvocationRuntimeApprovalRequest(
		ctx, *claim, effect, target, request,
	)
	if err != nil || approval.State != "pending" || approval.Version != 1 ||
		approval.ResolvedBy != "" || approval.Decision != "" {
		t.Fatalf("record approval = (%#v, %v)", approval, err)
	}
	replayed, err := repository.RecordInvocationRuntimeApprovalRequest(
		ctx, *claim, effect, target, request,
	)
	if err != nil || replayed.ID != approval.ID || replayed.Version != 1 {
		t.Fatalf("replay approval = (%#v, %v)", replayed, err)
	}
	var encodedApprovalEvent []byte
	if err := pool.QueryRow(ctx, `
		SELECT payload
		FROM invocation_events
		WHERE isolation_domain_id = $1
		  AND invocation_id = $2
		  AND source_kind = 'runtime'
		  AND source_sequence = $3
	`, domainID, invocationID, request.SourceSequence).Scan(&encodedApprovalEvent); err != nil {
		t.Fatal(err)
	}
	var approvalEvent map[string]any
	if err := json.Unmarshal(encodedApprovalEvent, &approvalEvent); err != nil {
		t.Fatal(err)
	}
	if approvalEvent["approvalId"] != approval.ID ||
		approvalEvent["approvalId"] == "approval-1" ||
		approvalEvent["action"] != request.RequestedAction {
		t.Fatalf("sanitized approval event = %#v", approvalEvent)
	}
	var approvalEventCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM invocation_events
		WHERE isolation_domain_id = $1
		  AND invocation_id = $2
		  AND source_kind = 'runtime'
		  AND source_sequence = $3
	`, domainID, invocationID, request.SourceSequence).Scan(&approvalEventCount); err != nil {
		t.Fatal(err)
	}
	if approvalEventCount != 1 {
		t.Fatalf("approval event replay count = %d", approvalEventCount)
	}
	conflict := request
	conflict.RequestedAction = "process.execute"
	if _, err := repository.RecordInvocationRuntimeApprovalRequest(
		ctx, *claim, effect, target, conflict,
	); !errors.Is(err, persistence.ErrInvocationRuntimeApprovalConflict) {
		t.Fatalf("conflicting request = %v", err)
	}
	resolution := persistence.InvocationRuntimeApprovalResolution{
		IsolationDomainID: domainID, InvocationID: invocationID,
		ApprovalID: approval.ID, ExpectedVersion: 1,
		Decision: "approve", ActorID: "actual-approver",
		CorrelationID: identity.New("cor"),
	}
	wrongInvocation := resolution
	wrongInvocation.InvocationID = identity.New("inv")
	wrongPathAuthorizations := 0
	if _, err := repository.ResolveInvocationRuntimeApprovalCommand(
		ctx,
		testIdempotency(domainID, "approval-resolution-wrong-path"),
		wrongInvocation,
		func(context.Context, persistence.InvocationRuntimeApproval) error {
			wrongPathAuthorizations++
			return nil
		},
	); !errors.Is(err, persistence.ErrInvocationRuntimeApprovalMissing) || wrongPathAuthorizations != 0 {
		t.Fatalf("wrong invocation path resolution = (%v), authorizations = %d", err, wrongPathAuthorizations)
	}
	commandIdempotency := testIdempotency(domainID, "approval-resolution-command")
	entryDenied := errors.New("entry authorization denied")
	if _, err := repository.ResolveInvocationRuntimeApprovalCommand(
		ctx,
		commandIdempotency,
		resolution,
		func(context.Context, persistence.InvocationRuntimeApproval) error { return entryDenied },
	); !errors.Is(err, entryDenied) {
		t.Fatalf("denied approval command = %v", err)
	}
	stillPending, err := repository.GetInvocationRuntimeApproval(ctx, domainID, approval.ID)
	if err != nil || stillPending.State != "pending" || stillPending.Version != 1 {
		t.Fatalf("approval after denied command = (%#v, %v)", stillPending, err)
	}
	var deniedResolutionEvents int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM invocation_events
		WHERE isolation_domain_id = $1
		  AND invocation_id = $2
		  AND event_type = 'interaction.approval.resolved'
	`, domainID, invocationID).Scan(&deniedResolutionEvents); err != nil {
		t.Fatal(err)
	}
	if deniedResolutionEvents != 0 {
		t.Fatalf("denied approval resolution event count = %d", deniedResolutionEvents)
	}
	authorizations := 0
	command, err := repository.ResolveInvocationRuntimeApprovalCommand(
		ctx,
		commandIdempotency,
		resolution,
		func(_ context.Context, candidate persistence.InvocationRuntimeApproval) error {
			authorizations++
			if candidate.InvocationID != invocationID ||
				candidate.Decision != resolution.Decision ||
				candidate.ResolvedBy != resolution.ActorID ||
				candidate.ResolutionCorrelationID != resolution.CorrelationID {
				t.Fatalf("authorization candidate = %#v", candidate)
			}
			return nil
		},
	)
	if err != nil || command.Status != http.StatusOK || command.Replayed {
		t.Fatalf("approval command = (%#v, %v)", command, err)
	}
	var public domain.InvocationApproval
	if err := json.Unmarshal(command.Body, &public); err != nil {
		t.Fatal(err)
	}
	if public.ID != approval.ID || public.InvocationID != invocationID ||
		public.State != "resolved" || public.Version != 2 ||
		public.Decision != resolution.Decision || public.ResolvedBy != resolution.ActorID ||
		public.ResolvedAt == nil {
		t.Fatalf("public approval = %#v", public)
	}
	var publicFields map[string]any
	if err := json.Unmarshal(command.Body, &publicFields); err != nil {
		t.Fatal(err)
	}
	for _, privateField := range []string{
		"operationId", "serviceId", "revisionId", "effectId", "sourceSequence",
		"effectiveDecision", "resolutionCorrelationId", "nativeApprovalId",
	} {
		if _, exposed := publicFields[privateField]; exposed {
			t.Fatalf("public approval exposed %q: %#v", privateField, publicFields)
		}
	}
	restarted := persistence.NewRepository(pool)
	restored, err := restarted.GetInvocationRuntimeApproval(ctx, domainID, approval.ID)
	if err != nil || restored.State != "resolved" || restored.Decision != "approve" ||
		restored.ResolvedBy != resolution.ActorID ||
		restored.ResolutionCorrelationID != resolution.CorrelationID {
		t.Fatalf("restore resolved approval = (%#v, %v)", restored, err)
	}
	if replayed, err := restarted.ResolveInvocationRuntimeApproval(
		ctx, resolution,
	); err != nil || replayed.Version != 2 {
		t.Fatalf("resource replay resolution = (%#v, %v)", replayed, err)
	}
	commandReplay, err := restarted.ResolveInvocationRuntimeApprovalCommand(
		ctx,
		commandIdempotency,
		resolution,
		func(context.Context, persistence.InvocationRuntimeApproval) error {
			authorizations++
			return nil
		},
	)
	if err != nil || !commandReplay.Replayed || authorizations != 1 ||
		string(commandReplay.Body) != string(command.Body) {
		t.Fatalf("approval command replay = (%#v, %v), authorizations = %d", commandReplay, err, authorizations)
	}
	reused := commandIdempotency
	reused.RequestDigest = sha256.Sum256([]byte("different approval decision"))
	if _, err := restarted.ResolveInvocationRuntimeApprovalCommand(
		ctx,
		reused,
		resolution,
		func(context.Context, persistence.InvocationRuntimeApproval) error { return nil },
	); err == nil || !strings.Contains(err.Error(), "IDEMPOTENCY_KEY_REUSED") {
		t.Fatalf("approval command idempotency reuse = %v", err)
	}
	var resolvedEventPayload []byte
	var resolvedEventActor, resolvedEventCorrelation string
	if err := pool.QueryRow(ctx, `
		SELECT payload, actor_id, correlation_id
		FROM invocation_events
		WHERE isolation_domain_id = $1
		  AND invocation_id = $2
		  AND event_type = 'interaction.approval.resolved'
	`, domainID, invocationID).Scan(
		&resolvedEventPayload, &resolvedEventActor, &resolvedEventCorrelation,
	); err != nil {
		t.Fatal(err)
	}
	var resolvedEvent map[string]any
	if err := json.Unmarshal(resolvedEventPayload, &resolvedEvent); err != nil {
		t.Fatal(err)
	}
	if len(resolvedEvent) != 3 ||
		resolvedEvent["approvalId"] != approval.ID ||
		resolvedEvent["decision"] != resolution.Decision ||
		resolvedEvent["version"] != float64(2) ||
		resolvedEventActor != resolution.ActorID ||
		resolvedEventCorrelation != resolution.CorrelationID {
		t.Fatalf(
			"approval resolution event = (%#v, actor %q, correlation %q)",
			resolvedEvent, resolvedEventActor, resolvedEventCorrelation,
		)
	}
	var resolvedEventCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM invocation_events
		WHERE isolation_domain_id = $1
		  AND invocation_id = $2
		  AND event_type = 'interaction.approval.resolved'
	`, domainID, invocationID).Scan(&resolvedEventCount); err != nil {
		t.Fatal(err)
	}
	if resolvedEventCount != 1 {
		t.Fatalf("approval resolution event count = %d", resolvedEventCount)
	}
	different := resolution
	different.CorrelationID = identity.New("cor")
	if _, err := restarted.ResolveInvocationRuntimeApproval(
		ctx, different,
	); !errors.Is(err, persistence.ErrInvocationRuntimeApprovalConflict) {
		t.Fatalf("conflicting resolution = %v", err)
	}
	delivering, err := restarted.BeginInvocationRuntimeApprovalDelivery(
		ctx, *claim, effect, approval.ID, "approve",
	)
	if err != nil || delivering.State != "delivering" || delivering.Version != 3 {
		t.Fatalf("begin delivery = (%#v, %v)", delivering, err)
	}
	if _, err := restarted.BeginInvocationRuntimeApprovalDelivery(
		ctx, *claim, effect, approval.ID, "approve",
	); !errors.Is(err, persistence.ErrInvocationRuntimeApprovalDeliveryAmbiguous) {
		t.Fatalf("duplicate delivery = %v", err)
	}
	delivered, err := restarted.CompleteInvocationRuntimeApprovalDelivery(
		ctx, *claim, effect, approval.ID,
	)
	if err != nil || delivered.State != "delivered" || delivered.Version != 4 ||
		delivered.EffectiveDecision != "approve" || delivered.DeliveredAt.IsZero() {
		t.Fatalf("complete delivery = (%#v, %v)", delivered, err)
	}
	replayedDelivery, err := restarted.CompleteInvocationRuntimeApprovalDelivery(
		ctx, *claim, effect, approval.ID,
	)
	if err != nil || replayedDelivery.Version != 4 {
		t.Fatalf("replay delivery completion = (%#v, %v)", replayedDelivery, err)
	}
	if _, err := restarted.GetInvocationRuntimeApproval(
		ctx, identity.New("iso"), approval.ID,
	); !errors.Is(err, persistence.ErrInvocationRuntimeApprovalMissing) {
		t.Fatalf("cross-domain approval = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		DELETE FROM invocation_runtime_approvals
		WHERE isolation_domain_id = $1 AND id = $2
	`, domainID, approval.ID); err == nil {
		t.Fatal("append-only approval accepted deletion")
	}
	var auditActor, auditCorrelation string
	var auditMetadata []byte
	if err := pool.QueryRow(ctx, `
		SELECT actor_id, correlation_id, safe_metadata
		FROM audit_records
		WHERE isolation_domain_id = $1
		  AND resource_type = 'invocation-approval'
		  AND resource_id = $2
		  AND action = 'invocation-approval.resolve'
	`, domainID, approval.ID).Scan(&auditActor, &auditCorrelation, &auditMetadata); err != nil {
		t.Fatal(err)
	}
	var safeAuditMetadata map[string]any
	if err := json.Unmarshal(auditMetadata, &safeAuditMetadata); err != nil {
		t.Fatal(err)
	}
	if auditActor != resolution.ActorID ||
		auditCorrelation != resolution.CorrelationID ||
		len(safeAuditMetadata) != 2 ||
		safeAuditMetadata["decision"] != resolution.Decision ||
		safeAuditMetadata["version"] != float64(2) {
		t.Fatalf(
			"approval audit = (actor %q, correlation %q, metadata %#v)",
			auditActor, auditCorrelation, safeAuditMetadata,
		)
	}
}
