package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/lifecycle/publication"
	"github.com/jackc/pgx/v5"
)

// QueueDevelopmentPublication accepts reviewed operator intent, not runtime
// evidence. This internal boundary does not grant public publication authority.
// The revision remains a draft until a fenced verifier commits publication.
func (repository *Repository) QueueDevelopmentPublication(ctx context.Context, idem Idempotency, input DevelopmentPublicationInput, deadline time.Time) (CommandResult, error) {
	if repository == nil || !repository.Configured() || ctx == nil || !input.Valid() || idem.IsolationDomainID != input.Target.IsolationDomainID || deadline.IsZero() {
		return CommandResult{}, ErrDevelopmentPublicationUnavailable
	}
	// Bind deployment-owned pins and actor to the command digest. Correlation
	// and a newly calculated deadline may differ on an otherwise exact retry.
	pins := input
	pins.CorrelationID = ""
	encoded, err := json.Marshal(struct {
		RequestDigest [32]byte
		Pins          DevelopmentPublicationInput
	}{idem.RequestDigest, pins})
	if err != nil {
		return CommandResult{}, ErrDevelopmentPublicationUnavailable
	}
	idem.RequestDigest = sha256.Sum256(encoded)
	return repository.execute(ctx, idem, func(tx pgx.Tx, _ time.Time) (int, any, error) {
		revision, err := getRevisionForUpdate(ctx, tx, input.Target.IsolationDomainID, input.Target.RevisionID)
		if err != nil {
			return 0, nil, err
		}
		if revision.ServiceID != input.Target.ServiceID || revision.State != "draft" || revision.Metadata.Version != input.ExpectedVersion || revision.RuntimeProfile != input.Target.RuntimeProfile || !slices.Equal(revision.RequiredCapabilities, []string{GovernedInvocationRuntimeProfile}) || domain.ValidateRevisionSchemas(revision.InputSchema, revision.OutputSchema) != nil {
			return 0, nil, ErrDevelopmentPublicationUnavailable
		}
		var now time.Time
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
			return 0, nil, err
		}
		if !deadline.After(now) || deadline.After(now.Add(24*time.Hour)) {
			return 0, nil, ErrDevelopmentPublicationUnavailable
		}
		operationID := identity.Derived("op", input.Target.IsolationDomainID+":"+input.Target.RevisionID+":governed-publication:v1")
		if _, err := tx.Exec(ctx, `INSERT INTO service_publication_operations (
            isolation_domain_id,id,revision_id,command,desired_state,observed_state,state_machine_version,generation,attempt,lease_token,due_at,deadline_at,correlation_id,actor_id,last_transition_at,created_at,updated_at
        ) VALUES ($1,$2,$3,'publish','published','queued',$4,1,0,0,$5,$6,$7,$8,$5,$5,$5)`, input.Target.IsolationDomainID, operationID, input.Target.RevisionID, publication.QueuedDevelopmentVersion, now, deadline, input.CorrelationID, input.ActorID); err != nil {
			return 0, nil, ErrDevelopmentPublicationUnavailable
		}
		if _, err := tx.Exec(ctx, `INSERT INTO governed_publication_requests (isolation_domain_id,operation_id,revision_id,service_id,expected_version,plan_digest,policy_digest,verification_digest,created_at)
            VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, input.Target.IsolationDomainID, operationID, input.Target.RevisionID, input.Target.ServiceID, input.ExpectedVersion, input.PlanDigest, input.PolicyDigest, input.VerificationDigest, now); err != nil {
			return 0, nil, err
		}
		if err := writeOutboxAndAudit(ctx, tx, input.Target.IsolationDomainID, OperationKindPublication, operationID, "service-publication.accepted", input.ActorID, input.CorrelationID, "accepted", operationID, now); err != nil {
			return 0, nil, err
		}
		operation, err := getPublicationOperation(ctx, tx, input.Target.IsolationDomainID, operationID)
		return http.StatusAccepted, operation, err
	})
}

// CompleteDevelopmentPublication verifies the queued pins under the exact live
// claim. Verification has only read effects. Publication, evidence and the final
// operation commit atomically; an uncertain commit is observed through the
// operation instead of repeating a native publication effect.
func (repository *Repository) CompleteDevelopmentPublication(ctx context.Context, claim OperationClaim, input DevelopmentPublicationInput, verify DevelopmentPublicationVerifier) error {
	if repository == nil || !repository.Configured() || ctx == nil || !input.Valid() || verify == nil || claim.Kind != OperationKindPublication || claim.StateMachineVersion != publication.QueuedDevelopmentVersion || claim.ObservedState != "validating" || (claim.Command != "publish" && claim.Command != "repair") || claim.IsolationDomainID != input.Target.IsolationDomainID || claim.ResourceID != input.Target.RevisionID || claim.ActorID != input.ActorID || claim.CorrelationID != input.CorrelationID {
		return ErrDevelopmentPublicationUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return ErrDevelopmentPublicationUnavailable
	}
	defer tx.Rollback(ctx)
	// Match the administrator's policy -> revision order before locking the
	// operation, matching repair's revision-before-operation order.
	if lockInvocationAuthorizationPolicyScope(ctx, tx, input.Target.IsolationDomainID, input.Target.ServiceID, input.Target.RevisionID) != nil {
		return ErrDevelopmentPublicationUnavailable
	}
	if _, err := getRevisionForUpdate(ctx, tx, input.Target.IsolationDomainID, input.Target.RevisionID); err != nil {
		return ErrDevelopmentPublicationUnavailable
	}
	var owner, state, command, actor, correlation, revision, service, plan, policy, verification string
	var token int64
	var version, expected int
	var leaseExpiry, deadline time.Time
	err = tx.QueryRow(ctx, `SELECT operation.lease_owner,operation.lease_token,operation.lease_expires_at,operation.deadline_at,
        operation.observed_state,operation.command,operation.state_machine_version,
        COALESCE(operation.effect_actor_id,operation.actor_id),COALESCE(operation.effect_correlation_id,operation.correlation_id),
        request.revision_id,request.service_id,request.expected_version,request.plan_digest,request.policy_digest,request.verification_digest
        FROM service_publication_operations operation JOIN governed_publication_requests request
          ON request.isolation_domain_id=operation.isolation_domain_id AND request.operation_id=operation.id
        WHERE operation.isolation_domain_id=$1 AND operation.id=$2 AND operation.revision_id=request.revision_id
        FOR UPDATE OF operation`, claim.IsolationDomainID, claim.ID).Scan(&owner, &token, &leaseExpiry, &deadline, &state, &command, &version, &actor, &correlation, &revision, &service, &expected, &plan, &policy, &verification)
	if err != nil || owner != claim.LeaseOwner || token != claim.FencingToken || state != claim.ObservedState || command != claim.Command || version != claim.StateMachineVersion || actor != claim.ActorID || correlation != claim.CorrelationID {
		return ErrLeaseLost
	}
	if revision != input.Target.RevisionID || service != input.Target.ServiceID || expected != input.ExpectedVersion || plan != input.PlanDigest || policy != input.PolicyDigest || verification != input.VerificationDigest {
		return ErrDevelopmentPublicationUnavailable
	}
	var now time.Time
	if tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now) != nil || !now.Before(leaseExpiry) || !now.Before(deadline) {
		return ErrLeaseLost
	}
	verificationCtx, stopVerification := context.WithDeadline(ctx, minPublicationDeadline(leaseExpiry, deadline))
	defer stopVerification()
	verified, err := verifyDevelopmentPublication(verificationCtx, tx, input, verify)
	if err != nil {
		return ErrDevelopmentPublicationUnavailable
	}
	if !verified.now.Before(leaseExpiry) || !verified.now.Before(deadline) {
		return ErrLeaseLost
	}
	receipt := map[string]any{"contract": DevelopmentPublicationContract, "isolationDomainId": input.Target.IsolationDomainID, "serviceId": input.Target.ServiceID, "revisionId": input.Target.RevisionID, "operationId": claim.ID, "state": "published", "certificationEligible": false}
	terminal, err := json.Marshal(receipt)
	if err != nil {
		return ErrDevelopmentPublicationUnavailable
	}
	result, err := tx.Exec(ctx, `UPDATE service_publication_operations SET observed_state='published',generation=generation+1,
        terminal_result=$6,lease_owner=NULL,lease_expires_at=NULL,due_at=$7,last_transition_at=$7,updated_at=$7
        WHERE isolation_domain_id=$1 AND id=$2 AND revision_id=$3 AND state_machine_version=3 AND observed_state='validating'
          AND lease_owner=$4 AND lease_token=$5 AND lease_expires_at>clock_timestamp() AND deadline_at>clock_timestamp()`, claim.IsolationDomainID, claim.ID, claim.ResourceID, claim.LeaseOwner, claim.FencingToken, terminal, verified.now)
	if err != nil {
		return ErrDevelopmentPublicationUnavailable
	}
	if result.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	if recordDevelopmentPublication(ctx, tx, input, claim.ID, verified) != nil {
		return ErrDevelopmentPublicationUnavailable
	}
	if err := tx.Commit(ctx); err != nil {
		return ErrDevelopmentPublicationUnavailable
	}
	return nil
}

func minPublicationDeadline(left, right time.Time) time.Time {
	if left.Before(right) {
		return left
	}
	return right
}
