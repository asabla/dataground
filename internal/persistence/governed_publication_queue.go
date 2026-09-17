package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/lifecycle/publication"
	"github.com/jackc/pgx/v5"
)

// QueueDevelopmentPublication accepts reviewed operator intent, not runtime
// evidence. This internal boundary does not grant public publication authority.
// The revision remains a draft until a fenced verifier commits publication.
func (repository *Repository) QueueDevelopmentPublication(ctx context.Context, idem Idempotency, input DevelopmentPublicationInput, deadline time.Time) (CommandResult, error) {
	return repository.queueDevelopmentPublication(ctx, idem, input, deadline, publication.QueuedDevelopmentVersion)
}

func (repository *Repository) queueDevelopmentPublication(ctx context.Context, idem Idempotency, input DevelopmentPublicationInput, deadline time.Time, version int) (CommandResult, error) {
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
	if version == publication.AuthorizedDevelopmentVersion {
		encoded = append([]byte("dataground.authorized-publication/v1\x00"), encoded...)
	}
	idem.RequestDigest = sha256.Sum256(encoded)
	return repository.execute(ctx, idem, func(tx pgx.Tx, _ time.Time) (int, any, error) {
		if version == publication.AuthorizedDevelopmentVersion {
			if err := lockInvocationAuthorizationPolicyScope(ctx, tx, input.Target.IsolationDomainID, input.Target.ServiceID, input.Target.RevisionID); err != nil {
				return 0, nil, ErrDevelopmentPublicationUnavailable
			}
			policy, err := getActiveInvocationAuthorizationPolicy(ctx, tx, input.Target.IsolationDomainID, input.Target.ServiceID, input.Target.RevisionID)
			if err != nil || policy.Contract != "dataground.invocation-authorization-policy/v5" || "sha256:"+hex.EncodeToString(policy.PolicyDigest) != input.PolicyDigest {
				return 0, nil, ErrDevelopmentPublicationUnavailable
			}
		}
		revision, err := getRevisionForUpdate(ctx, tx, input.Target.IsolationDomainID, input.Target.RevisionID)
		if err != nil {
			return 0, nil, err
		}
		if revision.ServiceID != input.Target.ServiceID || revision.State != "draft" || revision.Metadata.Version != input.ExpectedVersion || revision.RuntimeProfile != input.Target.RuntimeProfile || !slices.Equal(revision.RequiredCapabilities, []string{GovernedInvocationRuntimeProfile}) {
			return 0, nil, ErrDevelopmentPublicationUnavailable
		}
		if problem := domain.ValidateRevisionSchemas(revision.InputSchema, revision.OutputSchema); problem != nil {
			return 0, nil, &DomainError{Code: problem.Code, Message: problem.Message}
		}
		var now time.Time
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
			return 0, nil, err
		}
		if !deadline.After(now) || deadline.After(now.Add(24*time.Hour)) {
			return 0, nil, ErrDevelopmentPublicationUnavailable
		}
		operationID := developmentPublicationOperationID(input, version)
		if _, err := tx.Exec(ctx, `INSERT INTO service_publication_operations (
            isolation_domain_id,id,revision_id,command,desired_state,observed_state,state_machine_version,generation,attempt,lease_token,due_at,deadline_at,correlation_id,actor_id,last_transition_at,created_at,updated_at
        ) VALUES ($1,$2,$3,'publish','published','queued',$4,1,0,0,$5,$6,$7,$8,$5,$5,$5)`, input.Target.IsolationDomainID, operationID, input.Target.RevisionID, version, now, deadline, input.CorrelationID, input.ActorID); err != nil {
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

// RequireDevelopmentPublication matches a stored immutable request against the
// consumer's independently loaded configuration, including on terminal replay.
func (repository *Repository) RequireDevelopmentPublication(ctx context.Context, operationID string, input DevelopmentPublicationInput) error {
	return repository.requireDevelopmentPublication(ctx, operationID, input, publication.QueuedDevelopmentVersion)
}

func (repository *Repository) requireDevelopmentPublication(ctx context.Context, operationID string, input DevelopmentPublicationInput, version int) error {
	if repository == nil || !repository.Configured() || ctx == nil || !input.ValidReviewedInputs() {
		return ErrDevelopmentPublicationUnavailable
	}
	var matched bool
	err := repository.pool.QueryRow(ctx, `SELECT EXISTS (
        SELECT 1 FROM governed_publication_requests request
        JOIN service_publication_operations operation ON operation.isolation_domain_id=request.isolation_domain_id AND operation.id=request.operation_id
        JOIN service_revisions revision ON revision.isolation_domain_id=request.isolation_domain_id AND revision.id=request.revision_id
        WHERE request.isolation_domain_id=$1 AND request.operation_id=$2 AND request.service_id=$3 AND request.revision_id=$4
          AND request.expected_version=$5 AND request.plan_digest=$6 AND request.policy_digest=$7 AND request.verification_digest=$8
          AND operation.state_machine_version=$10 AND operation.revision_id=request.revision_id
          AND revision.service_id=request.service_id AND revision.runtime_profile=$9
    )`, input.Target.IsolationDomainID, operationID, input.Target.ServiceID, input.Target.RevisionID, input.ExpectedVersion, input.PlanDigest, input.PolicyDigest, input.VerificationDigest, input.Target.RuntimeProfile, version).Scan(&matched)
	if err != nil || !matched {
		return ErrDevelopmentPublicationUnavailable
	}
	return nil
}

// ClaimDevelopmentPublication leases only the exact reviewed version 3 request.
// It cannot consume another publication or invocation in the same revision.
func (repository *Repository) ClaimDevelopmentPublication(ctx context.Context, operationID string, input DevelopmentPublicationInput, workerID string, leaseDuration time.Duration) (*OperationClaim, error) {
	return repository.claimDevelopmentPublication(ctx, operationID, input, workerID, leaseDuration, publication.QueuedDevelopmentVersion)
}

func (repository *Repository) claimDevelopmentPublication(ctx context.Context, operationID string, input DevelopmentPublicationInput, workerID string, leaseDuration time.Duration, version int) (*OperationClaim, error) {
	if !validProviderCredentialText(workerID, 256) || leaseDuration <= 0 || leaseDuration > 2*time.Minute {
		return nil, ErrDevelopmentPublicationUnavailable
	}
	if err := repository.requireDevelopmentPublication(ctx, operationID, input, version); err != nil {
		return nil, err
	}
	return repository.claimNext(ctx, OperationKindPublication, input.Target.IsolationDomainID, input.Target.ServiceID, input.Target.RevisionID, input.Target.RuntimeProfile, workerID, leaseDuration, operationID, version)
}

// CompleteDevelopmentPublication verifies the queued pins under the exact live
// claim. Verification has only read effects. Publication, evidence and the final
// operation commit atomically; an uncertain commit is observed through the
// operation instead of repeating a native publication effect.
func (repository *Repository) CompleteDevelopmentPublication(ctx context.Context, claim OperationClaim, input DevelopmentPublicationInput, verify DevelopmentPublicationVerifier) error {
	return repository.completeDevelopmentPublication(ctx, claim, input, verify, publication.QueuedDevelopmentVersion, nil)
}

func (repository *Repository) completeDevelopmentPublication(ctx context.Context, claim OperationClaim, input DevelopmentPublicationInput, verify DevelopmentPublicationVerifier, expectedStateVersion int, authorize PublicationAuthorization) error {
	if repository == nil || !repository.Configured() || ctx == nil || !input.Valid() || verify == nil || claim.Kind != OperationKindPublication || claim.StateMachineVersion != expectedStateVersion || claim.ObservedState != "validating" || (claim.Command != "publish" && claim.Command != "repair") || claim.IsolationDomainID != input.Target.IsolationDomainID || claim.ResourceID != input.Target.RevisionID || claim.ActorID != input.ActorID || claim.CorrelationID != input.CorrelationID {
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
	policyContract := "dataground.invocation-authorization-policy/v3"
	var checkAuthority func(context.Context) error
	if expectedStateVersion == publication.AuthorizedDevelopmentVersion {
		if authorize == nil {
			return ErrDevelopmentPublicationUnavailable
		}
		policyContract = "dataground.invocation-authorization-policy/v5"
		checkAuthority = func(ctx context.Context) error {
			return authorize(ctx, publicationAuthorizationRequest(input, claim.ID, "effect", claim.FencingToken))
		}
	}
	verified, err := verifyDevelopmentPublication(verificationCtx, tx, input, verify, policyContract, checkAuthority)
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
        WHERE isolation_domain_id=$1 AND id=$2 AND revision_id=$3 AND state_machine_version=$8 AND observed_state='validating'
          AND lease_owner=$4 AND lease_token=$5 AND lease_expires_at>clock_timestamp() AND deadline_at>clock_timestamp()`, claim.IsolationDomainID, claim.ID, claim.ResourceID, claim.LeaseOwner, claim.FencingToken, terminal, verified.now, expectedStateVersion)
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

// PublicationAuthorization must be the deployment-owned audited authorizer.
// Callers cannot supply a public permit or replace this callback with request data.
type PublicationAuthorization func(context.Context, authz.PublicationRequest) error

func developmentPublicationOperationID(input DevelopmentPublicationInput, version int) string {
	suffix := ":governed-publication:v1"
	if version == publication.AuthorizedDevelopmentVersion {
		suffix = ":authorized-publication:v1"
	}
	return identity.Derived("op", input.Target.IsolationDomainID+":"+input.Target.RevisionID+suffix)
}

func publicationAuthorizationRequest(input DevelopmentPublicationInput, operationID, phase string, token int64) authz.PublicationRequest {
	return authz.PublicationRequest{ActorID: input.ActorID, IsolationDomainID: input.Target.IsolationDomainID, ServiceID: input.Target.ServiceID, RevisionID: input.Target.RevisionID, OperationID: operationID, CorrelationID: input.CorrelationID, Phase: phase, FencingToken: token, ExpectedVersion: int64(input.ExpectedVersion), PlanDigest: input.PlanDigest, VerificationDigest: input.VerificationDigest, PolicyDigest: input.PolicyDigest}
}

// QueueAuthorizedDevelopmentPublication checks current authority even before an
// idempotent replay. The transaction then pins that same active policy under its
// administrative lock; a changed policy cannot accept the earlier decision.
func (repository *Repository) QueueAuthorizedDevelopmentPublication(ctx context.Context, idem Idempotency, input DevelopmentPublicationInput, deadline time.Time, authorize PublicationAuthorization) (CommandResult, error) {
	if repository == nil || !repository.Configured() || ctx == nil || !input.Valid() || authorize == nil || idem.IsolationDomainID != input.Target.IsolationDomainID {
		return CommandResult{}, ErrDevelopmentPublicationUnavailable
	}
	if err := authorize(ctx, publicationAuthorizationRequest(input, developmentPublicationOperationID(input, publication.AuthorizedDevelopmentVersion), "entry", 0)); err != nil {
		return CommandResult{}, err
	}
	return repository.queueDevelopmentPublication(ctx, idem, input, deadline, publication.AuthorizedDevelopmentVersion)
}

func (repository *Repository) RequireAuthorizedDevelopmentPublication(ctx context.Context, operationID string, input DevelopmentPublicationInput) error {
	return repository.requireDevelopmentPublication(ctx, operationID, input, publication.AuthorizedDevelopmentVersion)
}

func (repository *Repository) ClaimAuthorizedDevelopmentPublication(ctx context.Context, operationID string, input DevelopmentPublicationInput, workerID string, leaseDuration time.Duration) (*OperationClaim, error) {
	return repository.claimDevelopmentPublication(ctx, operationID, input, workerID, leaseDuration, publication.AuthorizedDevelopmentVersion)
}

func (repository *Repository) CompleteAuthorizedDevelopmentPublication(ctx context.Context, claim OperationClaim, input DevelopmentPublicationInput, verify DevelopmentPublicationVerifier, authorize PublicationAuthorization) error {
	if authorize == nil {
		return ErrDevelopmentPublicationUnavailable
	}
	return repository.completeDevelopmentPublication(ctx, claim, input, verify, publication.AuthorizedDevelopmentVersion, authorize)
}

// RequirePublicationDispatchTarget permits an explicitly configured publishing
// API to start with its exact draft. Invocation-only startup keeps requiring a
// published revision. A published restart must match the retained v4 request.
func (repository *Repository) RequirePublicationDispatchTarget(ctx context.Context, input DevelopmentPublicationInput) error {
	if repository == nil || !repository.Configured() || ctx == nil || !input.ValidReviewedInputs() {
		return ErrDevelopmentPublicationUnavailable
	}
	var exists bool
	err := repository.pool.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM service_revisions revision WHERE revision.isolation_domain_id=$1 AND revision.service_id=$2 AND revision.id=$3
 AND revision.runtime_profile=$4 AND revision.required_capabilities=ARRAY[$4]::text[]
 AND ((revision.state='draft' AND revision.version=$5) OR (revision.state='published' AND EXISTS (
 SELECT 1 FROM governed_publication_requests request JOIN service_publication_operations operation
 ON operation.isolation_domain_id=request.isolation_domain_id AND operation.id=request.operation_id
 WHERE request.isolation_domain_id=$1 AND request.service_id=$2 AND request.revision_id=$3 AND request.expected_version=$5
 AND request.plan_digest=$6 AND request.policy_digest=$7 AND request.verification_digest=$8
 AND operation.revision_id=$3 AND operation.state_machine_version=4 AND operation.observed_state='published')))
 )`, input.Target.IsolationDomainID, input.Target.ServiceID, input.Target.RevisionID, input.Target.RuntimeProfile, input.ExpectedVersion, input.PlanDigest, input.PolicyDigest, input.VerificationDigest).Scan(&exists)
	if err != nil || !exists {
		return ErrDevelopmentPublicationUnavailable
	}
	return nil
}
